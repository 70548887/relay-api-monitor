function Write-AtomicJson([string]$Path, $Value) {
    $temporary = $Path + '.' + [guid]::NewGuid().ToString('N') + '.tmp'
    [IO.File]::WriteAllText($temporary, ($Value | ConvertTo-Json -Depth 8), [Text.UTF8Encoding]::new($false))
    Move-Item -LiteralPath $temporary -Destination $Path -Force
}

function Get-VerifiedProcess($Identity, [string]$ExpectedPath) {
    if ($null -eq $Identity -or -not $Identity.id -or -not $Identity.start_ticks) { return $null }
    $candidate = Get-Process -Id ([int]$Identity.id) -ErrorAction SilentlyContinue
    if ($null -eq $candidate) { return $null }
    try {
        if ([IO.Path]::GetFullPath($candidate.Path) -ieq [IO.Path]::GetFullPath($ExpectedPath) -and $candidate.StartTime.ToUniversalTime().Ticks -eq [long]$Identity.start_ticks) { return $candidate }
    } catch {}
    throw "进程身份不匹配，保留运行记录：PID $($Identity.id)"
}

function ConvertTo-NativeArgument([string]$Value) {
    if ($Value.Contains('"')) { throw '参数包含非法双引号' }
    return '"' + [regex]::Replace($Value, '(\\+)$', '$1$1') + '"'
}

function Get-LauncherConfiguration([string]$ProjectRoot, [string]$LauncherPath, [string[]]$Arguments) {
    Push-Location $ProjectRoot
    try {
        $content = & $LauncherPath @Arguments --print-config
        if ($LASTEXITCODE -ne 0) { throw '启动器不支持配置解析；v1.2.0/1.2.1 请先完成完整包迁移' }
        return ($content | ConvertFrom-Json)
    } finally { Pop-Location }
}
