param([ValidatePattern('^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$')][string]$Version='1.1.1', [switch]$Refresh)
$ErrorActionPreference='Stop'
$projectRoot=[System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$releaseRoot=Join-Path $projectRoot 'release'
$zipPath=Join-Path $releaseRoot ('relaylab-windows-amd64-'+$Version+'.zip')
if((Test-Path -LiteralPath $zipPath) -and -not $Refresh){throw '发布包已存在；确认更新时使用 -Refresh'}
New-Item -ItemType Directory -Path $releaseRoot -Force | Out-Null
$stageRoot=[System.IO.Path]::GetFullPath((Join-Path $releaseRoot ('.package-'+[guid]::NewGuid().ToString('N'))))
$releaseBoundary=[System.IO.Path]::GetFullPath($releaseRoot)+[System.IO.Path]::DirectorySeparatorChar
if(-not $stageRoot.StartsWith($releaseBoundary,[System.StringComparison]::OrdinalIgnoreCase)){throw '发布暂存目录超出release范围'}
$stage=Join-Path $stageRoot ('relaylab-windows-amd64-'+$Version)
$tempZip=$null
try {
New-Item -ItemType Directory -Path $stage -Force | Out-Null
New-Item -ItemType Directory -Path (Join-Path $stage 'bin') -Force | Out-Null
New-Item -ItemType Directory -Path (Join-Path $stage 'scripts') -Force | Out-Null
New-Item -ItemType Directory -Path (Join-Path $stage 'docs') -Force | Out-Null
Copy-Item -LiteralPath (Join-Path $projectRoot 'bin\relaylab.exe') -Destination (Join-Path $stage 'bin\relaylab.exe')
foreach($name in @('start.ps1','stop.ps1','backup.ps1','restore.ps1')){Copy-Item -LiteralPath (Join-Path $projectRoot ('scripts\'+$name)) -Destination (Join-Path $stage ('scripts\'+$name))}
foreach($name in @('openapi.json','VALIDATION.md','CHANGELOG.md','LIVE_TESTS.md')){if(Test-Path -LiteralPath (Join-Path $projectRoot ('docs\'+$name))){Copy-Item -LiteralPath (Join-Path $projectRoot ('docs\'+$name)) -Destination (Join-Path $stage ('docs\'+$name))}}
Copy-Item -LiteralPath (Join-Path $projectRoot 'README.md') -Destination (Join-Path $stage 'README.md')
if(Test-Path -LiteralPath (Join-Path $releaseRoot 'THIRD_PARTY_NOTICES.txt')){Copy-Item -LiteralPath (Join-Path $releaseRoot 'THIRD_PARTY_NOTICES.txt') -Destination (Join-Path $stage 'THIRD_PARTY_NOTICES.txt')}
$quickStart=@'
Relay Lab · 中转观测站

此包已包含编译后的Windows程序，无需安装Go或Node.js。

1. 右键 scripts/start.ps1，使用PowerShell运行。
   如果系统阻止脚本，在本目录运行：
   powershell -ExecutionPolicy Bypass -File .\scripts\start.ps1
2. 浏览器打开 http://127.0.0.1:42783。
3. 点击管理后台，创建首个管理员，然后添加平台和分组。
4. 停止时运行 scripts/stop.ps1。
5. 备份使用 scripts/backup.ps1；恢复使用 scripts/restore.ps1。

data目录会在首次运行时生成。保留数据库及master.key，或使用备份脚本完整备份。
详细功能和验证结果参见README.md与docs/VALIDATION.md。
'@
[System.IO.File]::WriteAllText((Join-Path $stage '开始使用.txt'),$quickStart,[System.Text.UTF8Encoding]::new($true))
$tempZip=Join-Path $releaseRoot ('relaylab-'+[guid]::NewGuid().ToString('N')+'.zip')
Compress-Archive -Path $stage -DestinationPath $tempZip
Move-Item -LiteralPath $tempZip -Destination $zipPath -Force
Write-Host "Windows发布包：$zipPath"
} finally {
    if(Test-Path -LiteralPath $stageRoot){Remove-Item -LiteralPath $stageRoot -Recurse -Force}
    if($tempZip -and (Test-Path -LiteralPath $tempZip)){Remove-Item -LiteralPath $tempZip -Force}
}
