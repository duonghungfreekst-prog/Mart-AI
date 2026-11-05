$ErrorActionPreference = 'SilentlyContinue'
$cf = 'C:\Program Files (x86)\cloudflared\cloudflared.exe'
if (-not (Test-Path $cf)) { exit 1 }

# Tat cac ban cu neu co
Get-Process -Name "cloudflared" -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue

$workDir = $PSScriptRoot
if (-not $workDir) { $workDir = 'F:\tools muse ai' }
$linkFile = Join-Path $workDir "LINK_KHACH_TRUY_CAP.txt"

$pinfo = New-Object System.Diagnostics.ProcessStartInfo
$pinfo.FileName = $cf
$pinfo.Arguments = "tunnel --url http://127.0.0.1:8088"
$pinfo.RedirectStandardError = $true
$pinfo.UseShellExecute = $false
$pinfo.CreateNoWindow = $true

$proc = New-Object System.Diagnostics.Process
$proc.StartInfo = $pinfo
$proc.Start() | Out-Null

$linkFound = $false
while (-not $proc.HasExited) {
    $line = $proc.StandardError.ReadLine()
    if ($line -match 'https://[a-zA-Z0-9-]+\.trycloudflare\.com') {
        $url = $matches[0]
        if (-not $linkFound) {
            $linkFound = $true
            $now = Get-Date -Format "dd/MM/yyyy HH:mm:ss"
            $content = @"
================================================================================
  LINK TRUY CAP TU XA CHO KHACH (CLOUDFLARE HTTPS - TU DONG KHOI CHAY)
  Cap nhat luc: $now
================================================================================

  👉  $url

================================================================================
  - Duong ham Cloudflare dang chay ngam 100% cung may chu.
  - Khach hang tu moi noi deu truy cap duoc qua duong link tren.
  - Link nay da duoc tu dong luu tai day moi khi may chu duoc bat.
================================================================================
"@
            [System.IO.File]::WriteAllText($linkFile, $content, [System.Text.UTF8Encoding]::new($false))
            try { Set-Clipboard -Value $url } catch {}
        }
    }
}
