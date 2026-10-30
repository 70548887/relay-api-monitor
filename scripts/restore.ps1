param([Parameter(Mandatory=$true)][string]$InputPath, [Parameter(Mandatory=$true)][string]$DataDir, [string]$IdentityFile = '')
$ErrorActionPreference = 'Stop'
$projectRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$restoreArgs = @('restore', '--input', $InputPath, '--data-dir', $DataDir)
if ($IdentityFile -ne '') { $restoreArgs += @('--identity-file', $IdentityFile) }
& (Join-Path $projectRoot 'bin\relaylab.exe') @restoreArgs
if ($LASTEXITCODE -ne 0) { throw '恢复失败' }
