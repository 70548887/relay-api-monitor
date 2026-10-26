param([string]$Address = '127.0.0.1:42783', [string]$DataDir = '', [switch]$NoBrowser)
$ErrorActionPreference = 'Stop'
$projectRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$exePath = Join-Path $projectRoot 'bin\relaylab.exe'
if (-not (Test-Path -LiteralPath $exePath)) { throw '尚未构建，请先运行 scripts/build.ps1' }
$addressUri=[Uri]('http://'+$Address)
if(-not $addressUri.IsAbsoluteUri -or $addressUri.AbsolutePath -ne '/' -or $addressUri.Query -ne '' -or $addressUri.Fragment -ne '' -or $addressUri.UserInfo -ne '' -or $Address -match '[\s"]'){throw '监听地址格式应为主机:端口'}
$healthHost=$addressUri.Host
if($healthHost -eq '0.0.0.0' -or $healthHost -eq '[::]'){ $healthHost='127.0.0.1' }
$healthAddress='http://'+$healthHost+':'+$addressUri.Port
$portProbe=[System.Net.Sockets.TcpClient]::new()
try {
    $portOpen=$portProbe.ConnectAsync($addressUri.DnsSafeHost,$addressUri.Port)
    if($portOpen.Wait(300) -and $portProbe.Connected){throw "端口已占用：$Address"}
} catch {
    if($_.Exception.Message -like '端口已占用*'){throw}
} finally {$portProbe.Dispose()}
if ($DataDir -eq '') { $DataDir = Join-Path $projectRoot 'data' }
$DataDir = [System.IO.Path]::GetFullPath($DataDir)
New-Item -ItemType Directory -Path $DataDir -Force | Out-Null
$pidPath = Join-Path $DataDir 'processes.json'
if (Test-Path -LiteralPath $pidPath) {
    $oldState = Get-Content -LiteralPath $pidPath -Raw | ConvertFrom-Json
    foreach ($item in $oldState.processes) {
        $running = Get-Process -Id $item.id -ErrorAction SilentlyContinue
        if ($null -ne $running -and $running.Path -eq $exePath -and $running.StartTime.ToUniversalTime().Ticks -eq [long]$item.start_ticks) { throw '程序已经运行，请先使用 scripts/stop.ps1 停止' }
    }
}
$arguments = @('serve', '--addr', $Address, '--data-dir', ('"' + $DataDir + '"'))
$webProcess = Start-Process -FilePath $exePath -ArgumentList $arguments -WorkingDirectory $projectRoot -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $DataDir 'serve.out.log') -RedirectStandardError (Join-Path $DataDir 'serve.err.log')
$workerProcess = $null
try {
    $ready = $false
    for ($attempt = 0; $attempt -lt 50; $attempt++) {
        if ($webProcess.HasExited) { throw '网站进程退出，请查看 data/serve.err.log' }
        try { $health = Invoke-RestMethod -Uri "$healthAddress/healthz" -TimeoutSec 1; if ($health.status -eq 'ok') { $ready = $true; break } } catch {}
        Start-Sleep -Milliseconds 200
    }
    if (-not $ready) { throw '网站启动超时，请查看 data/serve.err.log' }
    $workerProcess = Start-Process -FilePath $exePath -ArgumentList @('worker', '--data-dir', ('"' + $DataDir + '"')) -WorkingDirectory $projectRoot -WindowStyle Hidden -PassThru -RedirectStandardOutput (Join-Path $DataDir 'worker.out.log') -RedirectStandardError (Join-Path $DataDir 'worker.err.log')
    Start-Sleep -Milliseconds 300
    $webProcess.Refresh()
    if ($webProcess.HasExited) { throw '网站进程退出，可能端口被其他程序占用，请查看 data/serve.err.log' }
    if ($workerProcess.HasExited) { throw 'Worker 退出，请查看 data/worker.err.log' }
    $state = @{ processes = @(@{ id = $webProcess.Id; start_ticks = $webProcess.StartTime.ToUniversalTime().Ticks }, @{ id = $workerProcess.Id; start_ticks = $workerProcess.StartTime.ToUniversalTime().Ticks }); address = $Address }
    $tempState = $pidPath + '.tmp'
    [System.IO.File]::WriteAllText($tempState, ($state | ConvertTo-Json -Depth 4), [System.Text.UTF8Encoding]::new($false))
    Move-Item -LiteralPath $tempState -Destination $pidPath -Force
    Write-Host "Relay Lab 已启动：http://$Address"
    if (-not $NoBrowser) { Start-Process "http://$Address" }
} catch {
    if ($null -ne $workerProcess -and -not $workerProcess.HasExited) { Stop-Process -Id $workerProcess.Id }
    if (-not $webProcess.HasExited) { Stop-Process -Id $webProcess.Id }
    throw
}
