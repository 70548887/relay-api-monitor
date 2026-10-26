param([string]$DataDir = '')
$ErrorActionPreference = 'Stop'
$projectRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$exePath = Join-Path $projectRoot 'bin\relaylab.exe'
if ($DataDir -eq '') { $DataDir = Join-Path $projectRoot 'data' }
$pidPath = Join-Path ([System.IO.Path]::GetFullPath($DataDir)) 'processes.json'
if (-not (Test-Path -LiteralPath $pidPath)) { Write-Host '没有找到运行记录'; exit 0 }
$state = Get-Content -LiteralPath $pidPath -Raw | ConvertFrom-Json
[System.IO.File]::WriteAllText((Join-Path ([System.IO.Path]::GetFullPath($DataDir)) 'stop.request'), [DateTime]::UtcNow.ToString('O'))
foreach ($item in $state.processes) {
    $process = Get-Process -Id $item.id -ErrorAction SilentlyContinue
    if ($null -ne $process -and $process.Path -eq $exePath -and $process.StartTime.ToUniversalTime().Ticks -eq [long]$item.start_ticks) {
        if (-not $process.WaitForExit(15000)) { Stop-Process -Id $process.Id }
    }
}
Remove-Item -LiteralPath $pidPath
Write-Host 'Relay Lab 已停止。'
