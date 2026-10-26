$ErrorActionPreference='Stop'
$workspace=[System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$fixture=[System.IO.Path]::GetFullPath((Join-Path $env:TEMP ('relaylab-package-test-'+[guid]::NewGuid().ToString('N'))))
New-Item -ItemType Directory -Path $fixture -Force | Out-Null
foreach($dir in @('scripts','bin','docs','web\src','web\tests','cmd','internal','release','custom-private-data','release\relaylab-windows-amd64-9.9.9\data')){New-Item -ItemType Directory -Path (Join-Path $fixture $dir) -Force | Out-Null}
foreach($name in @('package.ps1','source-package.ps1','start.ps1','stop.ps1','backup.ps1','restore.ps1')){Copy-Item -LiteralPath (Join-Path $PSScriptRoot $name) -Destination (Join-Path $fixture ('scripts\'+$name))}
foreach($name in @('go.mod','go.sum','README.md','.gitignore','bin\relaylab.exe','docs\openapi.json','docs\VALIDATION.md','release\THIRD_PARTY_NOTICES.txt','web\package.json','web\package-lock.json','web\tsconfig.json','web\src\main.tsx','cmd\main.go','internal\schema.sql')){[IO.File]::WriteAllText((Join-Path $fixture $name),'fixture only')}
[IO.File]::WriteAllText((Join-Path $fixture 'release\relaylab-windows-amd64-9.9.9\data\master.key'),'must-not-ship')
[IO.File]::WriteAllText((Join-Path $fixture 'custom-private-data\credentials.json'),'must-not-ship')
[IO.File]::WriteAllText((Join-Path $fixture '.env.production'),'must-not-ship')
[IO.File]::WriteAllText((Join-Path $fixture 'internal\unexpected.key'),'must-not-ship')
& (Join-Path $fixture 'scripts\package.ps1') -Version '9.9.9' -Refresh
& (Join-Path $fixture 'scripts\source-package.ps1') -Version '9.9.9'
Add-Type -AssemblyName System.IO.Compression.FileSystem
foreach($name in @('relaylab-windows-amd64-9.9.9.zip','relaylab-source-9.9.9.zip')){
    $zip=[IO.Compression.ZipFile]::OpenRead((Join-Path $fixture ('release\'+$name)))
    try {
        foreach($entry in $zip.Entries){
            if($entry.FullName -match 'master\.key|credentials\.json|\.env|unexpected\.key|custom-private-data'){throw "发布包混入私密文件：$($entry.FullName)"}
        }
    } finally {$zip.Dispose()}
}
Write-Host 'PASS：刷新发布包排除旧暂存目录数据；源码包排除自定义私密目录、环境文件和密钥文件。'
# This isolated fixture remains in TEMP for inspection; no user directory is deleted.
