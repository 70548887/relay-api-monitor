param([string]$OutputPath = '', [string]$DataDir = '', [string]$Recipient = '')
$ErrorActionPreference = 'Stop'
$projectRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
if ($DataDir -eq '') { $DataDir = Join-Path $projectRoot 'data' }
if ($OutputPath -eq '') {
    $extension = '.zip'
    if ($Recipient -ne '') { $extension = '.zip.age' }
    $OutputPath = Join-Path $projectRoot ('backups\relaylab-' + [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssZ') + $extension)
}
$backupArgs = @('backup', '--data-dir', $DataDir, '--output', $OutputPath)
if ($Recipient -ne '') { $backupArgs += @('--recipient', $Recipient) }
& (Join-Path $projectRoot 'bin\relaylab.exe') @backupArgs
if ($LASTEXITCODE -ne 0) { throw '备份失败' }
