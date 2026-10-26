param([string]$Address = '127.0.0.1:42783', [string]$DataDir = '', [string]$ControlAddress = '127.0.0.1:42784', [string]$Config = '', [switch]$NoBrowser)
$ErrorActionPreference = 'Stop'
$projectRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$launcherPath = Join-Path $projectRoot 'bin\relaylab-launcher.exe'
$relayPath = Join-Path $projectRoot 'bin\relaylab.exe'
if (-not (Test-Path -LiteralPath $launcherPath)) { throw '尚未构建 relaylab-launcher.exe，请先运行 scripts/build.ps1' }
if (-not (Test-Path -LiteralPath $relayPath)) { throw '尚未构建 relaylab.exe，请先运行 scripts/build.ps1' }
if ($DataDir -eq '') { $DataDir = Join-Path $projectRoot 'data' }
if ($Config -eq '' -and (Test-Path -LiteralPath (Join-Path $projectRoot 'config.yaml'))) { $Config = Join-Path $projectRoot 'config.yaml' }
$DataDir = [System.IO.Path]::GetFullPath($DataDir)
New-Item -ItemType Directory -Path $DataDir -Force | Out-Null
$addressUri = [Uri]('http://' + $Address)
if (-not $addressUri.IsAbsoluteUri -or $addressUri.AbsolutePath -ne '/' -or $addressUri.Query -ne '' -or $addressUri.Fragment -ne '' -or $addressUri.UserInfo -ne '' -or $Address -match '[\s"]') { throw '监听地址格式应为主机:端口' }
$controlUri = [Uri]('http://' + $ControlAddress)
if (-not $controlUri.IsAbsoluteUri -or $controlUri.AbsolutePath -ne '/' -or $controlUri.Query -ne '' -or $controlUri.Fragment -ne '' -or $controlUri.UserInfo -ne '' -or $ControlAddress -match '[\s"]') { throw '控制地址格式应为主机:端口' }
$controlHost = $controlUri.Host
if ($controlHost -ne '127.0.0.1' -and $controlHost -ne 'localhost' -and $controlHost -ne '::1' -and $controlHost -ne '[::1]') { throw '控制地址必须绑定回环地址' }
$controlBase = 'http://' + $controlHost + ':' + $controlUri.Port
$pidPath = Join-Path $DataDir 'launcher.process.json'
if (Test-Path -LiteralPath $pidPath) {
    try {
        $old = Get-Content -LiteralPath $pidPath -Raw | ConvertFrom-Json
        $running = Get-Process -Id $old.id -ErrorAction SilentlyContinue
        if ($null -ne $running -and $running.Path -eq $launcherPath -and $running.StartTime.ToUniversalTime().Ticks -eq [long]$old.start_ticks) { throw '程序已经运行，请先使用 scripts/stop.ps1 停止' }
    } catch {
        if ($_.Exception.Message -like '程序已经运行*') { throw }
    }
}
$tokenPath = Join-Path $DataDir 'launcher.token'
if (Test-Path -LiteralPath $tokenPath) { Remove-Item -LiteralPath $tokenPath -Force }
$args = @('--relaylab', $relayPath, '--addr', $Address, '--data-dir', $DataDir, '--control-addr', $ControlAddress)
if ($Config -ne '') { $args += @('--config', [System.IO.Path]::GetFullPath($Config)) }
$launcher = Start-Process -FilePath $launcherPath -ArgumentList $args -WorkingDirectory $projectRoot -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $DataDir 'launcher.out.log') -RedirectStandardError (Join-Path $DataDir 'launcher.err.log')
try {
    $ready = $false
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
        if ($launcher.HasExited) { throw 'launcher 已退出，请查看 data/launcher.err.log' }
        if (Test-Path -LiteralPath $tokenPath) {
            $token = (Get-Content -LiteralPath $tokenPath -Raw).Trim()
            if ($token -ne '') {
                try {
                    $status = Invoke-RestMethod -Uri ($controlBase + '/status') -Headers @{ 'X-RelayLab-Launcher-Token' = $token } -TimeoutSec 1
                    if ($status.state -eq 'running') { $ready = $true; break }
                } catch {}
            }
        }
        Start-Sleep -Milliseconds 250
    }
    if (-not $ready) { throw '网站启动超时，请查看 data/launcher.err.log 和 data/serve.log' }
    $state = @{ id = $launcher.Id; start_ticks = $launcher.StartTime.ToUniversalTime().Ticks; address = $Address; control_address = $ControlAddress }
    $tempState = $pidPath + '.tmp'
    [System.IO.File]::WriteAllText($tempState, ($state | ConvertTo-Json -Depth 4), [System.Text.UTF8Encoding]::new($false))
    Move-Item -LiteralPath $tempState -Destination $pidPath -Force
    Write-Host "Relay Lab 已启动：http://$Address"
    if (-not $NoBrowser) { Start-Process "http://$Address" }
} catch {
    if ($null -ne $launcher -and -not $launcher.HasExited) { Stop-Process -Id $launcher.Id -Force }
    throw
}
