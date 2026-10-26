param([string]$Version = '1.1.1', [switch]$SkipInstall)
$ErrorActionPreference = 'Stop'
$projectRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
Push-Location $projectRoot
try {
    & go run ./cmd/specgen
    if ($LASTEXITCODE -ne 0) { throw '接口类型生成失败' }
    if (-not $SkipInstall) {
        Push-Location (Join-Path $projectRoot 'web')
        try { & npm.cmd ci; if ($LASTEXITCODE -ne 0) { throw '前端依赖安装失败' } } finally { Pop-Location }
    }
    Push-Location (Join-Path $projectRoot 'web')
    try { & npm.cmd run build; if ($LASTEXITCODE -ne 0) { throw '前端构建失败' } } finally { Pop-Location }
    New-Item -ItemType Directory -Path (Join-Path $projectRoot 'bin') -Force | Out-Null
    & go build -trimpath -ldflags "-s -w -X main.version=$Version" -o (Join-Path $projectRoot 'bin\relaylab.exe') ./cmd/relaylab
    if ($LASTEXITCODE -ne 0) { throw 'Go 构建失败' }
    Write-Host "构建完成：$(Join-Path $projectRoot 'bin\relaylab.exe')"
} finally { Pop-Location }
