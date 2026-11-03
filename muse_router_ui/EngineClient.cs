using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Net.Http;
using System.Text;
using System.Text.Json;
using System.Threading;
using System.Threading.Tasks;

namespace MuseRouter
{
    /// <summary>
    /// Bộ ghi log chuẩn PHAN 4 &amp; PHAN 12: Prefix bắt buộc `[UI HH:mm:ss]`, chống CRLF Injection, không dùng Console.WriteLine.
    /// </summary>
    public static class UiLogger
    {
        private static readonly object SyncLock = new object();

        public static string FormatLogLine(string level, string message)
        {
            string safe = (message ?? string.Empty).Replace("\r", " ").Replace("\n", " ");
            return $"[UI {DateTime.Now:HH:mm:ss}] [{level}] {safe}";
        }

        public static void Info(string message) => Write("INFO", message);
        public static void Warn(string message) => Write("WARN", message);
        public static void Error(string message, Exception? ex = null)
        {
            string detail = ex != null ? $"{message} ({ex.GetType().Name}: {ex.Message})" : message;
            Write("ERROR", detail);
        }

        private static void Write(string level, string message)
        {
            string line = FormatLogLine(level, message);
            Trace.WriteLine(line);
            lock (SyncLock)
            {
                try
                {
                    string logPath = Path.Combine(AppDomain.CurrentDomain.BaseDirectory, "muse_router_ui.log");
                    File.AppendAllText(logPath, line + Environment.NewLine, Encoding.UTF8);
                }
                catch (IOException ioEx)
                {
                    Trace.WriteLine($"[UI {DateTime.Now:HH:mm:ss}] [WARN] Log file write skipped: {ioEx.Message}");
                }
            }
        }
    }

    /// <summary>
    /// Bộ đệm L1 RAM In-process giới hạn SizeLimit = 1000 theo đúng PHAN 7.
    /// </summary>
    public sealed class BoundedMemoryCache<TValue>
    {
        public const int MaxSizeLimit = 1000;
        private readonly int _sizeLimit;
        private readonly ConcurrentDictionary<string, CacheEntry> _entries = new ConcurrentDictionary<string, CacheEntry>();

        private sealed class CacheEntry
        {
            public required TValue Value { get; init; }
            public required DateTimeOffset ExpiresAt { get; init; }
        }

        public BoundedMemoryCache(int sizeLimit = MaxSizeLimit)
        {
            _sizeLimit = sizeLimit > 0 && sizeLimit <= MaxSizeLimit ? sizeLimit : MaxSizeLimit;
        }

        public int Count => _entries.Count;

        public void Set(string key, TValue value, TimeSpan ttl)
        {
            if (string.IsNullOrWhiteSpace(key))
            {
                return;
            }
            if (_entries.Count >= _sizeLimit)
            {
                EvictExpiredOrOldest();
            }
            _entries[key] = new CacheEntry
            {
                Value = value,
                ExpiresAt = DateTimeOffset.UtcNow.Add(ttl)
            };
        }

        public bool TryGetValue(string key, out TValue? value)
        {
            value = default;
            if (!_entries.TryGetValue(key, out CacheEntry? entry) || entry == null)
            {
                return false;
            }
            if (DateTimeOffset.UtcNow > entry.ExpiresAt)
            {
                _entries.TryRemove(key, out _);
                return false;
            }
            value = entry.Value;
            return true;
        }

        private void EvictExpiredOrOldest()
        {
            DateTimeOffset now = DateTimeOffset.UtcNow;
            string? oldestKey = null;
            DateTimeOffset oldestTime = DateTimeOffset.MaxValue;

            foreach (KeyValuePair<string, CacheEntry> kvp in _entries)
            {
                if (now > kvp.Value.ExpiresAt)
                {
                    _entries.TryRemove(kvp.Key, out _);
                    return;
                }
                if (kvp.Value.ExpiresAt < oldestTime)
                {
                    oldestTime = kvp.Value.ExpiresAt;
                    oldestKey = kvp.Key;
                }
            }

            if (oldestKey != null)
            {
                _entries.TryRemove(oldestKey, out _);
            }
        }
    }

    public sealed class EngineSummaryDto
    {
        public int TotalNodes { get; set; }
        public int RunningNodes { get; set; }
        public int ReadyNodes { get; set; }
        public int UnavailableNodes { get; set; }
        public int ActiveRequests { get; set; }
        public int QueuedRequests { get; set; }
        public string FormattedTokens { get; set; } = "0";
        public string RoutingStrategy { get; set; } = "least_active";
    }

    /// <summary>
    /// Client giao tiếp 1 chiều `C# UI -> Go Engine` (PHAN 2 &amp; PHAN 7):
    /// - Dùng `using` cho mọi IDisposable
    /// - StreamReader buffer &lt;= 4096 bytes
    /// - Bọc try/catch OutOfMemoryException
    /// </summary>
    public sealed class GoEngineClient : IDisposable
    {
        private const int MaxStreamBufferBytes = 4096;
        private readonly HttpClient _httpClient;
        private readonly BoundedMemoryCache<EngineSummaryDto> _cache;
        private readonly string _baseUrl;
        private bool _disposed;

        public GoEngineClient(string baseUrl = "http://127.0.0.1:8088")
        {
            _baseUrl = baseUrl.TrimEnd('/');
            _httpClient = new HttpClient
            {
                Timeout = TimeSpan.FromSeconds(8)
            };
            _cache = new BoundedMemoryCache<EngineSummaryDto>(BoundedMemoryCache<EngineSummaryDto>.MaxSizeLimit);
        }

        public async Task<EngineSummaryDto?> FetchRouterSummaryAsync(bool forceRefresh = false, CancellationToken ct = default)
        {
            const string cacheKey = "router_summary";
            if (!forceRefresh && _cache.TryGetValue(cacheKey, out EngineSummaryDto? cached) && cached != null)
            {
                return cached;
            }

            try
            {
                using HttpRequestMessage req = new HttpRequestMessage(HttpMethod.Get, $"{_baseUrl}/api/router/summary");
                using HttpResponseMessage resp = await _httpClient.SendAsync(req, HttpCompletionOption.ResponseHeadersRead, ct).ConfigureAwait(false);
                if (!resp.IsSuccessStatusCode)
                {
                    UiLogger.Warn($"Go Engine summary returned HTTP {(int)resp.StatusCode}");
                    return null;
                }

                using Stream netStream = await resp.Content.ReadAsStreamAsync(ct).ConfigureAwait(false);
                // PHAN 7 BAT BUOC: StreamReader buffer <= 4096 bytes
                using StreamReader reader = new StreamReader(netStream, Encoding.UTF8, detectEncodingFromByteOrderMarks: true, bufferSize: MaxStreamBufferBytes, leaveOpen: false);
                string jsonText = await reader.ReadToEndAsync(ct).ConfigureAwait(false);

                using JsonDocument doc = JsonDocument.Parse(jsonText);
                JsonElement root = doc.RootElement;

                EngineSummaryDto dto = new EngineSummaryDto
                {
                    TotalNodes = root.TryGetProperty("total_nodes", out JsonElement tn) ? tn.GetInt32() : 0,
                    RunningNodes = root.TryGetProperty("running_nodes", out JsonElement rn) ? rn.GetInt32() : 0,
                    ReadyNodes = root.TryGetProperty("ready_nodes", out JsonElement rdn) ? rdn.GetInt32() : 0,
                    UnavailableNodes = root.TryGetProperty("unavailable_nodes", out JsonElement un) ? un.GetInt32() : 0,
                    ActiveRequests = root.TryGetProperty("active_requests", out JsonElement ar) ? ar.GetInt32() : 0,
                    QueuedRequests = root.TryGetProperty("queued_requests", out JsonElement qr) ? qr.GetInt32() : 0,
                    FormattedTokens = root.TryGetProperty("formatted_tokens", out JsonElement ft) ? (ft.GetString() ?? "0") : "0",
                    RoutingStrategy = root.TryGetProperty("routing_strategy", out JsonElement rs) ? (rs.GetString() ?? "least_active") : "least_active"
                };

                _cache.Set(cacheKey, dto, TimeSpan.FromSeconds(3));
                return dto;
            }
            catch (OutOfMemoryException oomEx)
            {
                GC.Collect();
                UiLogger.Error("OutOfMemoryException prevented while reading Go Engine stream", oomEx);
                return null;
            }
            catch (Exception ex)
            {
                UiLogger.Error("Failed to fetch summary from Go Engine", ex);
                return null;
            }
        }

        public void Dispose()
        {
            if (!_disposed)
            {
                _httpClient.Dispose();
                _disposed = true;
            }
        }
    }
}
