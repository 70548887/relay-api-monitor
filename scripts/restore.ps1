param([Parameter(Mandatory=$true)][string]$InputPath, [Parameter(Mandatory=$true)][string]$DataDir)
$ErrorActionPreference = 'Stop'
$projectRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
& (Join-Path $projectRoot 'bin\relaylab.exe') restore --input $InputPath --data-dir $DataDir
if ($LASTEXITCODE -ne 0) { throw '恢复失败' }
