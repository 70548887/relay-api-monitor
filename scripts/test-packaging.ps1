$ErrorActionPreference='Stop'
$fixture=[IO.Path]::GetFullPath((Join-Path $env:TEMP ('relaylab-package-test-'+[guid]::NewGuid().ToString('N'))))
New-Item -ItemType Directory -Path $fixture -Force | Out-Null
foreach($dir in @('scripts','bin','docs\releases','deploy','web\src','web\tests','cmd','internal','release','.github\workflows','custom-private-data','release\relaylab-windows-amd64-9.9.9\data')){New-Item -ItemType Directory -Path (Join-Path $fixture $dir) -Force | Out-Null}
foreach($name in @('package.ps1','source-package.ps1','release.py','start.ps1','stop.ps1','process-common.ps1','start.sh','backup.ps1','restore.ps1')){Copy-Item -LiteralPath (Join-Path $PSScriptRoot $name) -Destination (Join-Path $fixture ('scripts\'+$name))}
foreach($name in @('go.mod','go.sum','README.md','.gitignore','config.example.yaml','bin\relaylab.exe','bin\relaylab-launcher.exe','docs\openapi.json','docs\VALIDATION.md','docs\UPDATES.md','docs\RELEASE.md','docs\releases\v9.9.9.md','release\THIRD_PARTY_NOTICES.txt','web\package.json','web\package-lock.json','web\tsconfig.json','web\src\main.tsx','cmd\main.go','internal\schema.sql','deploy\relaylab-launcher.service','deploy\relaylab.env.example','.github\workflows\release.yml')){[IO.File]::WriteAllText((Join-Path $fixture $name),'fixture only')}
[IO.File]::WriteAllText((Join-Path $fixture 'VERSION'),"9.9.9`n")
$metadata=@{version='9.9.9';commit=('a'*40);build_time='2026-10-26T00:00:00Z'}|ConvertTo-Json
[IO.File]::WriteAllText((Join-Path $fixture 'bin\BUILD_METADATA.json'),$metadata)
foreach($name in @('release\relaylab-windows-amd64-9.9.9\data\master.key','custom-private-data\credentials.json','.env.production','internal\unexpected.key','deploy\relaylab.env','deploy\private.json','config.yaml')){[IO.File]::WriteAllText((Join-Path $fixture $name),'must-not-ship')}
& (Join-Path $fixture 'scripts\package.ps1') -Version '9.9.9' -Refresh
& (Join-Path $fixture 'scripts\source-package.ps1') -Version '9.9.9'
Add-Type -AssemblyName System.IO.Compression.FileSystem
foreach($name in @('relaylab-windows-amd64-9.9.9.zip','relaylab-source-9.9.9.zip')){
    $zip=[IO.Compression.ZipFile]::OpenRead((Join-Path $fixture ('release\'+$name)))
    try {
        $paths=@($zip.Entries|ForEach-Object {$_.FullName.Replace('\','/')})
        foreach($path in $paths){
            if($path -match 'master\.key|credentials\.json|\.env|unexpected\.key|custom-private-data|private\.json|/config\.yaml$' -and $path -notmatch 'relaylab\.env\.example$'){throw "发布包混入私密文件：$path"}
        }
        $prefix=if($name -like '*-source-*'){'relaylab-source-9.9.9'}else{'relaylab-windows-amd64-9.9.9'}
        foreach($required in @('VERSION','config.example.yaml','docs/UPDATES.md','docs/RELEASE.md','docs/releases/v9.9.9.md','deploy/relaylab-launcher.service','deploy/relaylab.env.example','scripts/process-common.ps1','THIRD_PARTY_NOTICES.txt')){if($paths -notcontains ($prefix+'/'+$required)){throw "发布包缺少必需文件：$required"}}
        if($name -like '*-source-*' -and $paths -notcontains ($prefix+'/.github/workflows/release.yml')){throw '源码包缺少 .github 工作流'}
        if($name -notlike '*-source-*'){foreach($required in @('bin/relaylab.exe','bin/relaylab-launcher.exe','scripts/start.ps1','scripts/stop.ps1')){if($paths -notcontains ($prefix+'/'+$required)){throw "完整包缺少：$required"}}}
    } finally {$zip.Dispose()}
}
Write-Host 'PASS：完整包必需文件、源码工作流/安全部署示例和私密文件排除均通过。'
Write-Host "隔离夹具：$fixture"
