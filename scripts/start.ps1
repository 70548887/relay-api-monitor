param([string]$Address = '', [string]$DataDir = '', [string]$ControlAddress = '', [string]$Config = '', [switch]$NoBrowser)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'process-common.ps1')
$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$launcherPath = Join-Path $projectRoot 'bin\relaylab-launcher.exe'
$relayPath = Join-Path $projectRoot 'bin\relaylab.exe'
foreach ($path in @($launcherPath, $relayPath)) { if (-not (Test-Path -LiteralPath $path)) { throw "缺少程序：$path；请先构建或解压完整发布包" } }
$arguments = @('--relaylab', $relayPath)
if ($PSBoundParameters.ContainsKey('Address')) { $arguments += @('--addr', $Address) }
if ($PSBoundParameters.ContainsKey('DataDir')) { $arguments += @('--data-dir', $DataDir) }
if ($PSBoundParameters.ContainsKey('ControlAddress')) { $arguments += @('--control-addr', $ControlAddress) }
if ($Config -eq '' -and -not $env:RELAYLAB_CONFIG -and (Test-Path -LiteralPath (Join-Path $projectRoot 'config.yaml'))) { $Config = Join-Path $projectRoot 'config.yaml' }
if ($Config -ne '') { $arguments += @('--config', [IO.Path]::GetFullPath($Config)) }
$resolved = Get-LauncherConfiguration $projectRoot $launcherPath $arguments
$DataDir = [IO.Path]::GetFullPath($resolved.data_dir)
New-Item -ItemType Directory -Path $DataDir -Force | Out-Null
$recordPath = Join-Path $DataDir 'launcher.process.json'
if (Test-Path -LiteralPath $recordPath) {
    $old = Get-Content -LiteralPath $recordPath -Raw | ConvertFrom-Json
    if ($null -ne (Get-VerifiedProcess $old $launcherPath)) { throw '程序已经运行，请先使用 scripts/stop.ps1 停止' }
    foreach ($child in $old.children) { if ($null -ne (Get-VerifiedProcess $child $relayPath)) { throw '仍有旧子进程运行，请先使用 scripts/stop.ps1 停止' } }
}
$legacyPath = Join-Path $DataDir 'processes.json'
if (Test-Path -LiteralPath $legacyPath) {
    $legacy = Get-Content -LiteralPath $legacyPath -Raw | ConvertFrom-Json
    foreach ($child in $legacy.processes) { if ($null -ne (Get-VerifiedProcess $child $relayPath)) { throw '旧版仍在运行，请先使用 scripts/stop.ps1 停止' } }
}
$nativeArguments = ($arguments | ForEach-Object { ConvertTo-NativeArgument $_ }) -join ' '
$launcher = Start-Process -FilePath $launcherPath -ArgumentList $nativeArguments -WorkingDirectory $projectRoot -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $DataDir 'launcher.out.log') -RedirectStandardError (Join-Path $DataDir 'launcher.err.log')
$record = @{ id=$launcher.Id; start_ticks=$launcher.StartTime.ToUniversalTime().Ticks; path=$launcherPath; address=$resolved.address; data_dir=$DataDir; children=@() }
Write-AtomicJson $recordPath $record
try {
    $ready = $false
    # A crashed worker can leave a 45-second lease. Allow launcher readiness
    # validation and the bounded lease wait to finish before declaring failure.
    $startupDeadline=[DateTime]::UtcNow.AddSeconds(120)
    while ([DateTime]::UtcNow -lt $startupDeadline) {
        $launcher.Refresh()
        if ($launcher.HasExited) { throw 'launcher 已退出，请查看数据目录中的 launcher.err.log' }
        $metadataPath = Join-Path $DataDir 'launcher.json'
        $tokenPath = Join-Path $DataDir 'launcher.token'
        if ((Test-Path -LiteralPath $metadataPath) -and (Test-Path -LiteralPath $tokenPath)) {
            try {
                $metadata = Get-Content -LiteralPath $metadataPath -Raw | ConvertFrom-Json
                if ([int]$metadata.pid -eq $launcher.Id -and $metadata.protocol -ge 2) {
                    $token = (Get-Content -LiteralPath $tokenPath -Raw).Trim()
                    $status = Invoke-RestMethod -Uri ('http://' + $metadata.control_address + '/status') -Headers @{ 'X-RelayLab-Launcher-Token'=$token } -TimeoutSec 1
                    $children = @()
                    foreach ($childStatus in @($status.serve,$status.worker)) {
                        if ($childStatus.pid) {
                            $child = Get-Process -Id $childStatus.pid -ErrorAction SilentlyContinue
                            if ($null -ne $child -and $child.Path -ieq $relayPath) { $children += @{id=$child.Id;start_ticks=$child.StartTime.ToUniversalTime().Ticks;path=$relayPath} }
                        }
                    }
                    $record.children=$children; $record.control_address=$metadata.control_address
                    Write-AtomicJson $recordPath $record
                    if ($status.state -eq 'running' -and $status.serve.running -and $status.worker.running) { $ready=$true; break }
                }
            } catch { if ($_.Exception.Message -like '进程身份不匹配*') { throw } }
        }
        Start-Sleep -Milliseconds 250
    }
    if (-not $ready) { throw '网站和 Worker 启动超时，请查看数据目录日志' }
    $url = 'http://' + $resolved.address
    Write-Host "Relay Lab 已启动：$url；启动器控制地址 $($record.control_address)"
    if (-not $NoBrowser) { Start-Process $url }
} catch {
    $startupError = $_
    try { & (Join-Path $PSScriptRoot 'stop.ps1') -DataDir $DataDir } catch { Write-Warning "清理未完成，运行记录已保留：$($_.Exception.Message)" }
    throw $startupError
}
