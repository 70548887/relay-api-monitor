$ErrorActionPreference = 'Stop'
$projectRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
Push-Location $projectRoot
try {
    & go test ./... -count=1 -timeout=120s
    if ($LASTEXITCODE -ne 0) { throw 'Go 测试失败' }
    & go vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'Go 静态检查失败' }
    & (Join-Path $PSScriptRoot 'test-packaging.ps1')
    if ($LASTEXITCODE -ne 0) { throw '发布文件隔离测试失败' }
    & python -B -m unittest discover -s scripts -p test_release.py -v
    if ($LASTEXITCODE -ne 0) { throw '发布契约与草稿发布测试失败' }
    Push-Location (Join-Path $projectRoot 'web')
    try {
        & npm.cmd test
        if ($LASTEXITCODE -ne 0) { throw '前端测试失败' }
        & npm.cmd run typecheck
        if ($LASTEXITCODE -ne 0) { throw 'TypeScript 检查失败' }
    } finally { Pop-Location }
} finally { Pop-Location }
