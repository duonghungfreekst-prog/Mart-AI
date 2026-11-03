using System;
using System.Windows;

namespace MuseRouter
{
    public partial class App : Application
    {
        protected override void OnStartup(StartupEventArgs e)
        {
            // Hỗ trợ chạy Unit Test thực tế qua tham số CLI `--run-tests`
            foreach (string arg in e.Args)
            {
                if (string.Equals(arg, "--run-tests", StringComparison.OrdinalIgnoreCase))
                {
                    int exitCode = MuseRouterSelfTests.RunAllTests();
                    Shutdown(exitCode);
                    return;
                }
            }

            base.OnStartup(e);
            UiLogger.Info("MuseRouter Desktop UI started");
        }
    }
}
