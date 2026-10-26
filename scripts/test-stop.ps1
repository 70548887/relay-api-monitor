$ErrorActionPreference='Stop'
# Isolated command mocks: no network request or real process termination.
$fixture=Join-Path $env:TEMP ('relaylab-stop-tests-'+[guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path (Join-Path $fixture 'scripts'),(Join-Path $fixture 'bin') -Force | Out-Null
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'stop.ps1') -Destination (Join-Path $fixture 'scripts\stop.ps1')
$helper=@'
function Get-VerifiedProcess($Identity,[string]$ExpectedPath){return $global:StopTestProcess}
function Get-LauncherConfiguration {throw 'explicit DataDir expected'}
'@
[IO.File]::WriteAllText((Join-Path $fixture 'scripts\process-common.ps1'),$helper,[Text.UTF8Encoding]::new($false))
$global:StopTestCalls=[Collections.Generic.List[string]]::new()
$global:StopTestProcess=[pscustomobject]@{Id=424242}
$global:StopTestProcess|Add-Member ScriptMethod WaitForExit {param($Timeout);$global:StopTestCalls.Add('wait');return $true}
function global:Stop-Process {param($Id,[switch]$Force);$global:StopTestCalls.Add('force')}
function global:Invoke-RestMethod {
    param($Uri,$Method,$Headers,$TimeoutSec)
    $path=([Uri]$Uri).AbsolutePath
    $global:StopTestCalls.Add($path)
    if($path -eq '/status'){
        if($global:StopTestMode -eq 'unreachable'){throw 'connection refused'}
        if($global:StopTestMode -eq 'invalid-status'){return [pscustomobject]@{}}
        $value=@{state='running';serve=@{pid=0;running=$true};worker=@{pid=0;running=$true}}
        if($global:StopTestMode -notin @('legacy-missing','legacy-one')){$value.protocol=2}
        if($global:StopTestMode -eq 'legacy-one'){$value.protocol=1}
        return [pscustomobject]$value
    }
    if($path -eq '/control/shutdown'){
        if($global:StopTestMode -eq 'protocol2-accepted'){return [pscustomobject]@{accepted=$true}}
        if($global:StopTestMode -eq 'protocol2-timeout'){throw 'request timed out after sending'}
        if($global:StopTestMode -like 'legacy-*'){throw '404 not found'}
        throw '409 installation in progress'
    }
    if($path -eq '/control/stop'){return [pscustomobject]@{state='stopped';serve=@{running=$false};worker=@{running=$false}}}
    throw "unexpected endpoint $path"
}
foreach($mode in @('protocol2-conflict','protocol2-timeout','unreachable','invalid-status','missing-token','legacy-missing','legacy-one','protocol2-accepted')){
    $global:StopTestMode=$mode
    $global:StopTestCalls.Clear()
    $data=Join-Path $fixture $mode
    New-Item -ItemType Directory -Path $data -Force | Out-Null
    [IO.File]::WriteAllText((Join-Path $data 'launcher.process.json'),'{"id":424242,"start_ticks":1,"control_address":"127.0.0.1:12345","children":[]}')
    if($mode -ne 'missing-token'){[IO.File]::WriteAllText((Join-Path $data 'launcher.token'),'test-token')}
    $failed=$false
    try{& (Join-Path $fixture 'scripts\stop.ps1') -DataDir $data}catch{$failed=$true}
    $recordExists=Test-Path -LiteralPath (Join-Path $data 'launcher.process.json')
    $markerExists=Test-Path -LiteralPath (Join-Path $data 'stop.request')
    if($mode -in @('legacy-missing','legacy-one')){
        if($failed -or $recordExists -or -not $markerExists -or $global:StopTestCalls -notcontains '/control/stop' -or $global:StopTestCalls -notcontains 'force'){throw "legacy regression: $mode"}
    }elseif($mode -eq 'protocol2-accepted'){
        if($failed -or $recordExists -or $markerExists -or $global:StopTestCalls -contains '/control/stop' -or $global:StopTestCalls -contains 'force'){throw 'protocol 2 accepted shutdown regression'}
    }else{
        if(-not $failed -or -not $recordExists -or $markerExists -or $global:StopTestCalls -contains '/control/stop' -or $global:StopTestCalls -contains 'force' -or $global:StopTestCalls -contains 'wait'){throw "ambiguous/rejected shutdown changed processes: $mode"}
    }
    Write-Host "PASS: $mode"
}
Write-Host "Stop tests passed: 8 scenarios. Fixture: $fixture"
