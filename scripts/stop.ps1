param([string]$DataDir = '', [string]$ControlAddress = '', [string]$Config = '')
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'process-common.ps1')
$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$launcherPath = Join-Path $projectRoot 'bin\relaylab-launcher.exe'
$relayPath = Join-Path $projectRoot 'bin\relaylab.exe'
if ($DataDir -eq '') {
    $arguments=@('--relaylab',$relayPath)
    if ($Config -eq '' -and -not $env:RELAYLAB_CONFIG -and (Test-Path -LiteralPath (Join-Path $projectRoot 'config.yaml'))) { $Config=Join-Path $projectRoot 'config.yaml' }
    if ($Config -ne '') { $arguments+=@('--config',[IO.Path]::GetFullPath($Config)) }
    try { $DataDir=(Get-LauncherConfiguration $projectRoot $launcherPath $arguments).data_dir } catch {
        $fallback=Join-Path $projectRoot 'data'
        if ((Test-Path -LiteralPath (Join-Path $fallback 'launcher.process.json')) -or (Test-Path -LiteralPath (Join-Path $fallback 'processes.json'))) { $DataDir=$fallback } else { throw }
    }
}
if (-not [IO.Path]::IsPathRooted($DataDir)) { $DataDir=Join-Path $projectRoot $DataDir }
$DataDir=[IO.Path]::GetFullPath($DataDir)
$recordPath=Join-Path $DataDir 'launcher.process.json'
$legacyPath=Join-Path $DataDir 'processes.json'
if (-not (Test-Path -LiteralPath $recordPath) -and -not (Test-Path -LiteralPath $legacyPath)) { Write-Host '没有找到运行记录。'; return }
# Protocol 2 owns graceful drain. Legacy/fallback paths use the file marker
# before waiting; writing it first would make managed children exit too early.
function Request-FileStop {
    [IO.File]::WriteAllText((Join-Path $DataDir 'stop.request'),[DateTime]::UtcNow.ToString('o'),[Text.UTF8Encoding]::new($false))
}
if (Test-Path -LiteralPath $recordPath) {
    $state=Get-Content -LiteralPath $recordPath -Raw | ConvertFrom-Json
    $process=Get-VerifiedProcess $state $launcherPath
    $children=@()
    foreach ($identity in $state.children) { $child=Get-VerifiedProcess $identity $relayPath; if ($null -ne $child) { $children+=$child } }
    if ($null -ne $process) {
        $shutdownSent=$false
        $confirmedLegacy=$false
        $metadataPath=Join-Path $DataDir 'launcher.json'
        if ($ControlAddress -eq '' -and (Test-Path -LiteralPath $metadataPath)) {
            $metadata=Get-Content -LiteralPath $metadataPath -Raw | ConvertFrom-Json
            if ($metadata.pid -and [int]$metadata.pid -ne $process.Id) { throw '启动器状态文件 PID 不匹配，保留运行记录' }
            $ControlAddress=$metadata.control_address
        }
        if ($ControlAddress -eq '') { $ControlAddress=$state.control_address }
        $tokenPath=Join-Path $DataDir 'launcher.token'
        if ($ControlAddress -and (Test-Path -LiteralPath $tokenPath)) {
            $uri=[Uri]('http://'+$ControlAddress)
            if ($uri.Host -notin @('127.0.0.1','localhost','::1','[::1]')) { throw '控制地址必须为回环地址' }
            $token=(Get-Content -LiteralPath $tokenPath -Raw).Trim()
            $headers=@{'X-RelayLab-Launcher-Token'=$token}
            $legacyStopped=$false
            try {
                $before=Invoke-RestMethod -Uri ('http://'+$ControlAddress+'/status') -Headers $headers -TimeoutSec 2
                if(-not $before.state -or $null -eq $before.serve -or $null -eq $before.worker){throw '启动器状态响应不完整'}
                $protocolProperty=$before.PSObject.Properties['protocol']
                $confirmedLegacy=($null -eq $protocolProperty -or [string]$protocolProperty.Value -match '^[01]$')
                foreach($childStatus in @($before.serve,$before.worker)){
                    if($childStatus.pid){
                        $child=Get-Process -Id $childStatus.pid -ErrorAction SilentlyContinue
                        if($null -ne $child -and $child.Path -ieq $relayPath){$children+=$child}
                    }
                }
                Invoke-RestMethod -Method Post -Uri ('http://'+$ControlAddress+'/control/shutdown') -Headers $headers -TimeoutSec 3 | Out-Null
                $shutdownSent=$true
            } catch {
                if(-not $confirmedLegacy){throw "启动器关闭请求被拒绝或结果未确认，已保留运行记录：$($_.Exception.Message)"}
                # Protocol 1 has no shutdown endpoint. Stop children first and
                # terminate that verified launcher only after their exit.
                Request-FileStop
                try {
                    $stopped=Invoke-RestMethod -Method Post -Uri ('http://'+$ControlAddress+'/control/stop') -Headers $headers -TimeoutSec 25
                    $legacyStopped=($stopped.state -eq 'stopped' -and -not $stopped.serve.running -and -not $stopped.worker.running)
                } catch { Write-Warning '启动器关闭请求未确认，继续等待优雅退出。' }
            }
            if($legacyStopped){
                foreach($child in $children){if(-not $child.WaitForExit(5000)){throw "旧启动器子进程仍在运行：PID $($child.Id)"}}
                $process=Get-VerifiedProcess $state $launcherPath
                if($null -ne $process){Stop-Process -Id $process.Id -Force}
            }
        }
        if(-not $shutdownSent){
            if(-not $confirmedLegacy){throw '未确认旧协议，停止未完成，已保留运行记录。'}
            Request-FileStop
        }
        if ($null -ne $process -and -not $process.WaitForExit(45000)) { throw "启动器仍在运行，保留记录以便重试：PID $($process.Id)" }
    }
    foreach ($child in $children) { if (-not $child.WaitForExit(5000)) { throw "子进程仍在运行，保留记录：PID $($child.Id)" } }
    Remove-Item -LiteralPath $recordPath -Force
}
if (Test-Path -LiteralPath $legacyPath) {
    Request-FileStop
    $legacy=Get-Content -LiteralPath $legacyPath -Raw | ConvertFrom-Json
    foreach ($identity in $legacy.processes) {
        $process=Get-VerifiedProcess $identity $relayPath
        if ($null -ne $process -and -not $process.WaitForExit(15000)) {
            # Legacy children have no launcher; force only the verified identity.
            $process=Get-VerifiedProcess $identity $relayPath
            if ($null -ne $process) { Stop-Process -Id $process.Id -Force; if (-not $process.WaitForExit(5000)) { throw "旧版进程仍在运行：PID $($process.Id)" } }
        }
    }
    Remove-Item -LiteralPath $legacyPath -Force
}
Write-Host 'Relay Lab 已停止，网站与 Worker 均已退出。'
