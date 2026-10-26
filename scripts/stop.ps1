param([string]$DataDir = '', [string]$ControlAddress = '')
$ErrorActionPreference = 'Stop'
$projectRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$launcherPath = Join-Path $projectRoot 'bin\relaylab-launcher.exe'
$legacyExePath = Join-Path $projectRoot 'bin\relaylab.exe'
if ($DataDir -eq '') { $DataDir = Join-Path $projectRoot 'data' }
$DataDir = [System.IO.Path]::GetFullPath($DataDir)
$pidPath = Join-Path $DataDir 'launcher.process.json'
if (-not (Test-Path -LiteralPath $pidPath)) {
    $legacyPath = Join-Path $DataDir 'processes.json'
    if (Test-Path -LiteralPath $legacyPath) {
        $legacy = Get-Content -LiteralPath $legacyPath -Raw | ConvertFrom-Json
        foreach ($item in $legacy.processes) {
            $process = Get-Process -Id $item.id -ErrorAction SilentlyContinue
            if ($null -ne $process -and $process.Path -eq $legacyExePath -and $process.StartTime.ToUniversalTime().Ticks -eq [long]$item.start_ticks) {
                if (-not $process.WaitForExit(15000)) { Stop-Process -Id $process.Id -Force }
            }
        }
        Remove-Item -LiteralPath $legacyPath -Force -ErrorAction SilentlyContinue
        Write-Host '旧版 Relay Lab 已停止。请重新运行 scripts/start.ps1 切换到 launcher。'
        exit 0
    }
    Write-Host '没有找到运行记录'; exit 0
}
$state = Get-Content -LiteralPath $pidPath -Raw | ConvertFrom-Json
if ($ControlAddress -eq '') { $ControlAddress = $state.control_address }
$controlUri = [Uri]('http://' + $ControlAddress)
$controlBase = 'http://' + $controlUri.Host + ':' + $controlUri.Port
$tokenPath = Join-Path $DataDir 'launcher.token'
if (Test-Path -LiteralPath $tokenPath) {
    $token = (Get-Content -LiteralPath $tokenPath -Raw).Trim()
    if ($token -ne '') {
        try { Invoke-RestMethod -Method Post -Uri ($controlBase + '/control/stop') -Headers @{ 'X-RelayLab-Launcher-Token' = $token } -TimeoutSec 25 | Out-Null } catch {}
    }
}
$process = Get-Process -Id $state.id -ErrorAction SilentlyContinue
if ($null -ne $process -and $process.Path -eq $launcherPath -and $process.StartTime.ToUniversalTime().Ticks -eq [long]$state.start_ticks) {
    if (-not $process.WaitForExit(15000)) { Stop-Process -Id $process.Id -Force }
}
Remove-Item -LiteralPath $pidPath -Force -ErrorAction SilentlyContinue
Write-Host 'Relay Lab 已停止。'
