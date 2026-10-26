param([string]$OutputPath = '', [string]$DataDir = '')
$ErrorActionPreference = 'Stop'
$projectRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
if ($DataDir -eq '') { $DataDir = Join-Path $projectRoot 'data' }
if ($OutputPath -eq '') { $OutputPath = Join-Path $projectRoot ('backups\relaylab-' + (Get-Date -Format 'yyyyMMdd-HHmmss') + '.zip') }
& (Join-Path $projectRoot 'bin\relaylab.exe') backup --data-dir $DataDir --output $OutputPath
if ($LASTEXITCODE -ne 0) { throw '备份失败' }
