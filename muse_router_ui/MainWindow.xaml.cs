using System;
using System.Diagnostics;
using System.Windows;
using System.Windows.Threading;

namespace MuseRouter
{
    public partial class MainWindow : Window
    {
        private readonly GoEngineClient _engineClient;
        private readonly DispatcherTimer _pollTimer;

        public MainWindow()
        {
            InitializeComponent();
            _engineClient = new GoEngineClient("http://127.0.0.1:8088");
            _pollTimer = new DispatcherTimer
            {
                Interval = TimeSpan.FromSeconds(4)
            };
            _pollTimer.Tick += async (_, _) => await RefreshEngineTelemetryAsync(false);

            Loaded += async (_, _) =>
            {
                AppendUiLog("Khởi tạo giao diện điều khiển MuseRouter. Kết nối tới Go Engine tại http://127.0.0.1:8088...");
                await RefreshEngineTelemetryAsync(true);
                _pollTimer.Start();
            };

            Closed += (_, _) =>
            {
                _pollTimer.Stop();
                _engineClient.Dispose();
            };
        }

        private void AppendUiLog(string message)
        {
            string line = UiLogger.FormatLogLine("INFO", message);
            UiLogger.Info(message);
            TxtActivityLog.AppendText(line + Environment.NewLine);
            TxtActivityLog.ScrollToEnd();
        }

        private async System.Threading.Tasks.Task RefreshEngineTelemetryAsync(bool forceRefresh)
        {
            EngineSummaryDto? summary = await _engineClient.FetchRouterSummaryAsync(forceRefresh);
            if (summary == null)
            {
                TxtTokensRemain.Text = "0";
                TxtRunningNodes.Text = "0";
                TxtReadyNodes.Text = "0";
                TxtTotalNodes.Text = "0";
                if (forceRefresh)
                {
                    AppendUiLog("Chưa kết nối được Go Engine (127.0.0.1:8088). Hãy chạy run_muse_router.bat để khởi động dịch vụ.");
                }
                return;
            }

            TxtTokensRemain.Text = summary.FormattedTokens;
            TxtRunningNodes.Text = summary.RunningNodes.ToString();
            TxtReadyNodes.Text = summary.ReadyNodes.ToString();
            TxtTotalNodes.Text = summary.TotalNodes.ToString();

            if (forceRefresh)
            {
                AppendUiLog($"Đồng bộ thành công: {summary.TotalNodes} Node ({summary.RunningNodes} running, {summary.ReadyNodes} ready) · Tổng {summary.FormattedTokens} tokens · Chiến lược: {summary.RoutingStrategy}");
            }
        }

        private void BtnOpenWebStudio_Click(object sender, RoutedEventArgs e)
        {
            try
            {
                ProcessStartInfo psi = new ProcessStartInfo
                {
                    FileName = "http://127.0.0.1:8088",
                    UseShellExecute = true
                };
                Process.Start(psi);
                AppendUiLog("Đã mở MuseRouter Web Studio trên trình duyệt mặc định.");
            }
            catch (Exception ex)
            {
                UiLogger.Error("Không thể mở trình duyệt", ex);
                AppendUiLog("Lỗi mở trình duyệt: " + ex.Message);
            }
        }

        private async void BtnRefreshStatus_Click(object sender, RoutedEventArgs e)
        {
            await RefreshEngineTelemetryAsync(true);
        }
    }
}
