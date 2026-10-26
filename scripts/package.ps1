param([string]$Version = '', [switch]$Refresh)
$ErrorActionPreference = 'Stop'
$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$expected = ([IO.File]::ReadAllText((Join-Path $projectRoot 'VERSION'))).Trim()
if ($Version -eq '') { $Version = $expected }
if ($Version -ne $expected) { throw 'Version 必须与 VERSION 文件一致' }
$zip = Join-Path $projectRoot ('release\relaylab-windows-amd64-' + $Version + '.zip')
if ((Test-Path -LiteralPath $zip) -and -not $Refresh) { throw '发布包已存在；更新时使用 -Refresh' }
$metadataPath = Join-Path $projectRoot 'bin\BUILD_METADATA.json'
if (-not (Test-Path -LiteralPath $metadataPath)) { throw '缺少构建元数据，请先运行 scripts/build.ps1' }
$metadata = Get-Content -LiteralPath $metadataPath -Raw | ConvertFrom-Json
if ($metadata.version -ne $Version) { throw '现有二进制版本与 VERSION 不一致，请重新构建' }
$buildTime=([DateTimeOffset]$metadata.build_time).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
& python (Join-Path $PSScriptRoot 'release.py') --root $projectRoot --output (Join-Path $projectRoot 'release') --binary-dir (Join-Path $projectRoot 'bin') --skip-build --windows-only --local-names --commit $metadata.commit --build-time $buildTime
if ($LASTEXITCODE -ne 0) { throw '发布打包或校验失败' }
Write-Host "Windows 完整包：$zip"
