param([ValidatePattern('^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$')][string]$Version='1.1.1')
$ErrorActionPreference='Stop'
$projectRoot=[System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$releaseRoot=Join-Path $projectRoot 'release'
$stageRoot=[System.IO.Path]::GetFullPath((Join-Path $releaseRoot ('.source-package-'+[guid]::NewGuid().ToString('N'))))
$stage=Join-Path $stageRoot ('relaylab-source-'+$Version)
$tempArchive=Join-Path $releaseRoot ('relaylab-source-'+[guid]::NewGuid().ToString('N')+'.zip')
New-Item -ItemType Directory -Path $stage -Force | Out-Null
Push-Location $projectRoot
try {
    # Explicit source roots prevent custom data/backup directories from being
    # swept into a source release merely because they are outside data/.
    $files=@('go.mod','go.sum','README.md','.gitignore')
    $sourceFiles=& rg --files --hidden cmd internal scripts docs web/src web/tests -g '!*.db' -g '!*.db-*' -g '!*.sqlite*' -g '!*.key' -g '!.env*' -g '!*.log' -g '!*.tmp'
    if($LASTEXITCODE -ne 0){throw '源码文件枚举失败'}
    $files += $sourceFiles
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
