param([string]$DataDir = '', [string]$ControlAddress = '', [string]$Binary = '')
$ErrorActionPreference = 'Stop'
$projectRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
if ($DataDir -eq '') { $DataDir = Join-Path $projectRoot 'data' }
$DataDir = [System.IO.Path]::GetFullPath($DataDir)
$pidPath = Join-Path $DataDir 'launcher.process.json'
if (-not (Test-Path -LiteralPath $pidPath)) { throw '没有找到运行中的 launcher' }
$state = Get-Content -LiteralPath $pidPath -Raw | ConvertFrom-Json
if ($ControlAddress -eq '') { $ControlAddress = $state.control_address }
$controlUri = [Uri]('http://' + $ControlAddress)
$controlBase = 'http://' + $controlUri.Host + ':' + $controlUri.Port
$token = (Get-Content -LiteralPath (Join-Path $DataDir 'launcher.token') -Raw).Trim()
$headers = @{ 'X-RelayLab-Launcher-Token' = $token }
if ($Binary -ne '') {
    throw '在线安装请通过后台升级接口执行；此脚本仅提供 launcher 重启入口。'
}
Invoke-RestMethod -Method Post -Uri ($controlBase + '/control/restart') -Headers $headers -TimeoutSec 40 | ConvertTo-Json -Depth 10
