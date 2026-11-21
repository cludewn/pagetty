param([string]$ScriptDir = $PSScriptRoot)
$ErrorActionPreference = 'Stop'

function Assert($ok, $message) { if (!$ok) { throw $message } }
function Assert-Fails([scriptblock]$test, $pattern) {
    try { & $test | Out-Null } catch {
        Assert ($_.Exception.Message -match $pattern) "Unexpected error: $_"
        return
    }
    throw "Expected failure: $pattern"
}

$installer = [IO.Path]::Combine($ScriptDir, 'install.cmd')
$text = [IO.File]::ReadAllText($installer)
$marker = [char]10 + ':payload'
$offset = $text.IndexOf($marker)
Assert ($offset -ge 0) 'Installer marker missing'
$code = $text.Substring($offset + $marker.Length)
$null = [scriptblock]::Create($code)
. ([scriptblock]::Create(($code -split '(?m)^# Command entry point\r?$', 2)[0]))
Initialize-Native

$base = Join-Path ([IO.Path]::GetTempPath()) ('PageTTY test [' + [guid]::NewGuid().ToString('N') + ']')
$source = Join-Path $base 'extracted'
$root = Join-Path $base 'Local AppData'
$dir = Join-Path $root 'PageTTY'
$work = Join-Path $root 'PageTTY.setup'
$launcher = Join-Path $source 'install.cmd'
$installed = Join-Path $dir 'uninstall.cmd'

function New-FakeKey($kind, $raw) {
    $obj = [pscustomobject]@{ Kind = $kind; Raw = $raw; Writes = 0; Fail = '' }
    $obj | Add-Member ScriptMethod GetValueNames { if ($this.Kind -ne 'Absent') { 'Path' } }
    $obj | Add-Member ScriptMethod GetValueKind { $this.Kind }
    $obj | Add-Member ScriptMethod GetValue {
        param($name, $default, $options)
        if ($options -ne 'DoNotExpandEnvironmentNames') { throw 'Expected raw PATH read' }
        $this.Raw
    }
    $obj | Add-Member ScriptMethod SetValue {
        param($name, $value, $kind)
        $this.Writes++
        if ($this.Fail -eq 'before') { throw 'Injected registry write failure' }
        $this.Kind = [string]$kind; $this.Raw = $value
        if ($this.Fail -eq 'after' -or $this.Fail -eq 'rollback') {
            if ($this.Fail -eq 'after') { $this.Fail = '' } else { $this.Fail = 'before' }
            throw 'Injected registry write failure'
        }
    }
    $obj | Add-Member ScriptMethod DeleteValue { $this.Writes++; $this.Kind = 'Absent'; $this.Raw = '' }
    $obj
}

function Install($key, $effective) {
    Invoke-PageTTYSetup $root $source $launcher $key $effective $false
}
function Uninstall($key) {
    Assert ([IO.File]::Exists($installed)) 'Installed uninstall.cmd missing'
    Invoke-PageTTYSetup $root $dir $installed $key '' $true
}
function Snapshot {
    if ([IO.Directory]::Exists($dir)) {
        ([IO.Directory]::GetFiles($dir) | Sort-Object | ForEach-Object {
            [IO.Path]::GetFileName($_) + ': ' + (Get-FileHash -LiteralPath $_).Hash
        }) -join ';'
    }
}
function Set-Version($version) {
    foreach ($name in @('pagetty.exe','LICENSE','THIRD_PARTY_NOTICES.txt')) {
        [IO.File]::WriteAllText((Join-Path $source $name), "$version $name")
    }
    [IO.File]::Copy($installer, $launcher, $true)
}
function Expect-Unchanged($key, $before, $snapshot) {
    Assert ((Get-PathHash (Read-Path $key)) -ceq $before) 'PATH value or type changed'
    Assert ((Snapshot) -ceq $snapshot) 'Files or ownership record changed'
    Assert (![IO.Directory]::Exists($work)) 'A setup directory was left behind'
}

[IO.Directory]::CreateDirectory($source) | Out-Null
[IO.Directory]::CreateDirectory($root) | Out-Null
try {
    Set-Version first
    $raw = '%CUSTOM%;C:\User;;'
    $key = New-FakeKey ExpandString $raw
    $before = Get-PathHash (Read-Path $key)
    $atLimit = 'x' * (8191 - $dir.Length - 1)
    $plan = Get-PathPlan (Read-Path $key) $atLimit $dir $null $false
    Assert ($plan.Change -and $plan.Value.Kind -eq 'ExpandString') '8191 characters should fit'
    Assert-Fails { Install $key ($atLimit + 'x') } '8191-character limit'
    Expect-Unchanged $key $before $null
    Assert (!$key.Writes) 'Long effective PATH reached registry write'
    $null = Install $key 'C:\Machine;C:\Expanded;C:\User;;'
    Assert ($key.Raw -ceq "$raw;$dir" -and $key.Kind -eq 'ExpandString') 'Raw value or type was rewritten'
    $first = Snapshot
    $null = Install $key "C:\Machine;C:\Expanded;C:\User;;;$dir"
    Assert ((Snapshot) -ceq $first -and $key.Writes -eq 1) 'Reinstall was not idempotent'
    $registered = Get-PathHash (Read-Path $key)

    foreach ($name in @('pagetty.exe','LICENSE','THIRD_PARTY_NOTICES.txt','uninstall.cmd')) {
        $path = Join-Path $dir $name
        $original = [IO.File]::ReadAllBytes($path)
        [IO.File]::WriteAllText($path, 'user replacement')
        $replacement = Snapshot
        Assert-Fails { Install $key $dir } 'Managed file has changed'
        Assert-Fails { Uninstall $key } 'Managed file has changed'
        Expect-Unchanged $key $registered $replacement
        [IO.File]::WriteAllBytes($path, $original)
    }

    Set-Version second
    & {
        function Copy-PayloadFile($from, $to) {
            if ($to.EndsWith('THIRD_PARTY_NOTICES.txt')) { throw 'Injected copy failure' }
            [IO.File]::Copy($from, $to)
        }
        Assert-Fails { Install $key $dir } 'Injected copy failure'
        Expect-Unchanged $key $registered $first
    }
    & {
        function Save-Record($file, $state) { throw 'Injected ownership record failure' }
        Assert-Fails { Install $key $dir } 'Injected ownership record failure'
        Expect-Unchanged $key $registered $first
    }
    & {
        function Move-Payload($from, $to) {
            if ($from.EndsWith('\new')) { throw 'Injected placement failure' }
            [IO.Directory]::Move($from, $to)
        }
        Assert-Fails { Install $key $dir } 'Injected placement failure'
        Expect-Unchanged $key $registered $first
    }
    $null = Install $key $dir
    Assert (([IO.File]::ReadAllText((Join-Path $dir 'LICENSE'))) -eq 'second LICENSE') 'Update did not replace documents'
    $second = Snapshot

    $key.Fail = 'before'
    Assert-Fails { Uninstall $key } 'Injected registry write failure'
    Expect-Unchanged $key $registered $second
    $key.Fail = 'after'
    Assert-Fails { Uninstall $key } 'Injected registry write failure'
    Expect-Unchanged $key $registered $second

    [IO.Directory]::Delete($source, $true)
    $null = Uninstall $key
    Expect-Unchanged $key $before $null

    [IO.Directory]::CreateDirectory($source) | Out-Null
    Set-Version first
    foreach ($case in @('duplicate','edited','moved','type')) {
        $key = New-FakeKey String 'C:\Other'
        $null = Install $key 'C:\Machine;C:\Other'
        switch ($case) {
            duplicate { $key.Raw += ";$dir" }
            edited { $key.Raw += ';C:\UserAdded' }
            moved { $key.Raw = "$dir;C:\Other" }
            type { $key.Kind = 'ExpandString' }
        }
        $expected = Get-PathHash (Read-Path $key)
        $null = Uninstall $key
        Expect-Unchanged $key $expected $null
        Assert ($key.Writes -eq 1) 'An ambiguous PATH was rewritten'
    }

    $key = New-FakeKey String ('C:\Existing;' + $dir)
    $before = Get-PathHash (Read-Path $key)
    $null = Install $key $key.Raw
    $null = Uninstall $key
    Expect-Unchanged $key $before $null
    Assert (!$key.Writes) 'A preexisting PATH entry became owned'
    $key = New-FakeKey Absent ''
    $before = Get-PathHash (Read-Path $key)
    $null = Install $key 'C:\Machine'
    $null = Uninstall $key
    Expect-Unchanged $key $before $null

    $key = New-FakeKey String 'C:\Keep'
    $before = Get-PathHash (Read-Path $key)
    $key.Fail = 'after'
    Assert-Fails { Install $key 'C:\Machine;C:\Keep' } 'Injected registry write failure'
    Expect-Unchanged $key $before $null

    $key.Fail = 'rollback'
    Assert-Fails { Install $key 'C:\Machine;C:\Keep' } 'Rollback incomplete.*Recovery directory'
    Assert ([IO.Directory]::Exists($work)) 'Recovery directory was removed after rollback failure'
    Assert (![IO.Directory]::Exists($dir)) 'Payload rollback did not complete'

    # The batch fixture keeps the launcher but swaps its payload for a harmless rename.
    $fixture = Join-Path $base 'launch (folder) & !'
    $moved = Join-Path $base 'launched'
    [IO.Directory]::CreateDirectory($fixture) | Out-Null
    $fake = Join-Path $fixture 'uninstall.cmd'
    $header = $text.Substring(0, $offset + $marker.Length).Replace('start "PageTTY uninstall"', 'start /b "PageTTY uninstall"')
    $body = @'
$ErrorActionPreference = 'Stop'
if ($env:PAGETTY_UNINSTALL -ne '1') { throw 'Wrong worker mode.' }
$folder = [IO.Path]::GetDirectoryName($env:PAGETTY_SETUP)
[IO.Directory]::Move($folder, [IO.Path]::Combine([IO.Path]::GetDirectoryName($folder), 'launched'))
Write-Output 'LAUNCHER WORKER OK'
'@
    [IO.File]::WriteAllText($fake, (($header + [char]10 + $body) -replace '\r?\n', ([string][char]13 + [char]10)))
    $info = [Diagnostics.ProcessStartInfo]::new()
    $info.FileName = Join-Path $env:SystemRoot 'System32\cmd.exe'
    $info.Arguments = '/d /c ""' + $fake + '""'
    $info.WorkingDirectory = $base
    $info.UseShellExecute = $false
    $info.RedirectStandardInput = $true
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    $info.CreateNoWindow = $true
    $proc = [Diagnostics.Process]::Start($info)
    try {
        $stdout = $proc.StandardOutput.ReadToEndAsync()
        $stderr = $proc.StandardError.ReadToEndAsync()
        $proc.StandardInput.WriteLine('')
        $proc.StandardInput.Close()
        if (!$proc.WaitForExit(15000)) { $proc.Kill(); throw 'Batch fixture timed out.' }
        if (!$stdout.Wait(15000) -or !$stderr.Wait(15000)) { throw 'Batch worker timed out.' }
        $output = $stdout.Result + $stderr.Result
        Assert ($proc.ExitCode -eq 0 -and $output -match 'LAUNCHER WORKER OK' -and [IO.Directory]::Exists($moved)) $output
    } finally { $proc.Dispose() }
    Write-Host 'Windows installer tests passed with temporary files, a fake registry and a batch fixture.'

} finally {
    [IO.Directory]::Delete($base, $true)
}
