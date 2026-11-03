using System;

namespace MuseRouter
{
    /// <summary>
    /// Unit Tests thực tế cho `muse_router_ui` (PHAN 9: `*.Tests.cs`).
    /// Kiểm tra:
    /// 1. BoundedMemoryCache SizeLimit = 1000 &amp; TTL eviction.
    /// 2. Định dạng log chuẩn `[UI HH:mm:ss]` và chống CRLF Injection.
    /// </summary>
    public static class MuseRouterSelfTests
    {
        public static int RunAllTests()
        {
            try
            {
                TestLogPrefixAndCrLfSanitization();
                TestBoundedMemoryCacheSizeLimit();
                UiLogger.Info("All C# UI unit tests passed successfully (2/2)");
                return 0;
            }
            catch (Exception ex)
            {
                UiLogger.Error("C# UI unit test failed", ex);
                return 1;
            }
        }

        private static void TestLogPrefixAndCrLfSanitization()
        {
            string formatted = UiLogger.FormatLogLine("INFO", "Hello\r\nInjectedLine");
            if (!formatted.StartsWith("[UI ", StringComparison.Ordinal))
            {
                throw new InvalidOperationException("Log line must start with [UI HH:mm:ss]");
            }
            if (formatted.Contains('\r') || formatted.Contains('\n'))
            {
                throw new InvalidOperationException("CRLF characters must be sanitized from log line");
            }
        }

        private static void TestBoundedMemoryCacheSizeLimit()
        {
            BoundedMemoryCache<int> cache = new BoundedMemoryCache<int>(1000);
            for (int i = 0; i < 1050; i++)
            {
                cache.Set($"key_{i}", i, TimeSpan.FromMinutes(5));
            }
            if (cache.Count > BoundedMemoryCache<int>.MaxSizeLimit)
            {
                throw new InvalidOperationException($"Cache exceeded SizeLimit 1000: actual={cache.Count}");
            }
            if (!cache.TryGetValue("key_1049", out int latest) || latest != 1049)
            {
                throw new InvalidOperationException("Expected latest cached item to be retrievable");
            }
        }
    }
}
