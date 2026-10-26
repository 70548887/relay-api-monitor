param([string]$Version = '', [switch]$SkipInstall)
$ErrorActionPreference = 'Stop'
$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$expected = ([IO.File]::ReadAllText((Join-Path $projectRoot 'VERSION'))).Trim()
if ($Version -eq '') { $Version = $expected }
if ($Version -ne $expected) { throw 'Version 必须与 VERSION 文件一致' }
$previousCGO = $env:CGO_ENABLED
Push-Location $projectRoot
try {
    $commit = (& git rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0) { throw '读取构建 commit 失败' }
    $buildTime = [DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ')
    $flags = "-s -w -X main.version=$Version -X main.commit=$commit -X main.buildTime=$buildTime"
    $env:CGO_ENABLED = '0'
    & go run ./cmd/specgen
    if ($LASTEXITCODE -ne 0) { throw '接口类型生成失败' }
    if (-not $SkipInstall) {
        & npm.cmd --prefix web ci
        if ($LASTEXITCODE -ne 0) { throw '前端依赖安装失败' }
    }
    & npm.cmd --prefix web run build
    if ($LASTEXITCODE -ne 0) { throw '前端构建失败' }
    & node (Join-Path $PSScriptRoot 'notices.mjs')
    if ($LASTEXITCODE -ne 0) { throw '第三方声明生成失败' }
    New-Item -ItemType Directory -Path (Join-Path $projectRoot 'bin') -Force | Out-Null
    foreach ($program in @('relaylab', 'relaylab-launcher')) {
        & go build -trimpath -ldflags $flags -o (Join-Path $projectRoot ('bin\' + $program + '.exe')) ('./cmd/' + $program)
        if ($LASTEXITCODE -ne 0) { throw "$program 构建失败" }
    }
    $metadata = @{ version=$Version; commit=$commit; build_time=$buildTime } | ConvertTo-Json
    $target = Join-Path $projectRoot 'bin\BUILD_METADATA.json'
    [IO.File]::WriteAllText(($target + '.tmp'), $metadata, [Text.UTF8Encoding]::new($false))
    Move-Item -LiteralPath ($target + '.tmp') -Destination $target -Force
    Write-Host "构建完成：$Version ($commit)，构建时间 $buildTime"
} finally { $env:CGO_ENABLED=$previousCGO; Pop-Location }
