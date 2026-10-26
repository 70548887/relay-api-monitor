param([ValidatePattern('^\d+\.\d+\.\d+$')][string]$Version='')
$ErrorActionPreference='Stop'
$projectRoot=[System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$expected=([IO.File]::ReadAllText((Join-Path $projectRoot 'VERSION'))).Trim()
if($Version -eq ''){$Version=$expected}
if($Version -ne $expected){throw 'Version 必须与 VERSION 文件一致'}
$releaseRoot=Join-Path $projectRoot 'release'
$stageRoot=[System.IO.Path]::GetFullPath((Join-Path $releaseRoot ('.source-package-'+[guid]::NewGuid().ToString('N'))))
$releaseBoundary=[IO.Path]::GetFullPath($releaseRoot)+[IO.Path]::DirectorySeparatorChar
if(-not $stageRoot.StartsWith($releaseBoundary,[StringComparison]::OrdinalIgnoreCase)){throw '源码暂存目录超出 release 范围'}
$stage=Join-Path $stageRoot ('relaylab-source-'+$Version)
$tempArchive=Join-Path $releaseRoot ('relaylab-source-'+[guid]::NewGuid().ToString('N')+'.zip')
New-Item -ItemType Directory -Path $stage -Force | Out-Null
Push-Location $projectRoot
try {
    # Explicit source roots prevent custom data/backup directories from being
    # swept into a source release merely because they are outside data/.
    $files=@('VERSION','go.mod','go.sum','README.md','.gitignore','config.example.yaml')
    $sourceFiles=& rg --files --hidden cmd internal scripts docs web/src web/tests .github -g '!*.db' -g '!*.db-*' -g '!*.sqlite*' -g '!*.key' -g '!.env*' -g '!*.log' -g '!*.tmp' -g '!*.pyc' -g '!__pycache__/**'
    if($LASTEXITCODE -ne 0){throw '源码文件枚举失败'}
    $files += $sourceFiles
    $files += Get-ChildItem -LiteralPath (Join-Path $projectRoot 'deploy') -File | Where-Object {$_.Name -match '\.(service|example|md)$'} | ForEach-Object {'deploy\'+$_.Name}
    $files += Get-ChildItem -LiteralPath (Join-Path $projectRoot 'web') -File | Where-Object { $_.Name -match '^(package(-lock)?\.json|tsconfig[^/]*\.json|index\.html|vite\.config\.ts|vitest\.config\.ts|playwright\.config\.ts)$' } | ForEach-Object { 'web\'+$_.Name }
    foreach($relative in $files){
        $source=Join-Path $projectRoot $relative
        $destination=Join-Path $stage $relative
        New-Item -ItemType Directory -Path (Split-Path -Parent $destination) -Force | Out-Null
        Copy-Item -LiteralPath $source -Destination $destination
    }
    Copy-Item -LiteralPath (Join-Path $projectRoot 'release\THIRD_PARTY_NOTICES.txt') -Destination (Join-Path $stage 'THIRD_PARTY_NOTICES.txt')
    $archive=Join-Path $projectRoot ('release\relaylab-source-'+$Version+'.zip')
    Compress-Archive -Path $stage -DestinationPath $tempArchive
    Move-Item -LiteralPath $tempArchive -Destination $archive -Force
    Write-Host "源码包：$archive"
} finally {Pop-Location;if(Test-Path -LiteralPath $stageRoot){Remove-Item -LiteralPath $stageRoot -Recurse -Force};if(Test-Path -LiteralPath $tempArchive){Remove-Item -LiteralPath $tempArchive -Force}}
