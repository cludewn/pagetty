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
$notifications = [Collections.Generic.List[bool]]::new()
function Send-PathNotification([bool]$uninstall) { $notifications.Add($uninstall) }

$base = Join-Path ([IO.Path]::GetTempPath()) ('PageTTY test [' + [guid]::NewGuid().ToString('N') + ']')
$source = Join-Path $base 'extracted'
$appData = Join-Path $base 'AppData'
$root = Join-Path $appData 'Local'
$dir = Join-Path $root 'PageTTY'
$work = Join-Path $root 'PageTTY.setup'
$launcher = Join-Path $source 'install.cmd'
$installed = Join-Path $dir 'uninstall.cmd'

function New-FakeKey($kind, $raw, [bool]$present = $true) {
    $obj = [pscustomobject]@{ Kind = $kind; Raw = $raw; Writes = 0; Fail = ''; Present = $present; Creates = 0; Denied = '' }
    $obj | Add-Member ScriptMethod OpenSubKey {
        param($name, $writable)
        if ($name -cne 'Environment') { throw 'Unexpected registry key opened' }
        if ($this.Denied -eq 'read' -or ($writable -and $this.Denied -eq 'write')) {
            throw [UnauthorizedAccessException]::new('Injected registry access denial')
        }
        if ($this.Present) { return $this }
    }
    $obj | Add-Member ScriptMethod CreateSubKey {
        param($name)
        if ($name -cne 'Environment' -or $this.Present) { throw 'Unexpected registry key creation' }
        $this.Present = $true; $this.Creates++
        $this
    }
    $obj | Add-Member ScriptMethod Dispose {}
    $obj | Add-Member ScriptMethod GetValueNames { if ($this.Kind -ne 'Absent') { 'Path' } }
    $obj | Add-Member ScriptMethod GetValueKind { $this.Kind }
    $obj | Add-Member ScriptMethod GetValue {
        param($name, $default, $options)
        if ($name -cne 'Path') { throw 'Unexpected registry value read' }
        if ($options -ne 'DoNotExpandEnvironmentNames') { throw 'Expected raw PATH read' }
        $this.Raw
    }
    $obj | Add-Member ScriptMethod SetValue {
        param($name, $value, $kind)
        if ($name -cne 'Path') { throw 'Unexpected registry value write' }
        $this.Writes++
        if ($this.Fail -eq 'before') { throw 'Injected registry write failure' }
        $this.Kind = [string]$kind; $this.Raw = $value
        if ($this.Fail -eq 'after' -or $this.Fail -eq 'rollback') {
            if ($this.Fail -eq 'after') { $this.Fail = '' } else { $this.Fail = 'before' }
            throw 'Injected registry write failure'
        }
    }
    $obj | Add-Member ScriptMethod DeleteValue {
        param($name, $throwOnMissing)
        if ($name -cne 'Path') { throw 'Unexpected registry value delete' }
        $this.Writes++; $this.Kind = 'Absent'; $this.Raw = ''
    }
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
    # Windows normally marks AppData as hidden. The provider and .NET parent have different shapes.
    [IO.File]::SetAttributes($appData, [IO.File]::GetAttributes($appData) -bor [IO.FileAttributes]::Hidden)
    $rootItem = Get-Item -LiteralPath $root -Force
    $parent = $rootItem.Parent
    Assert ($rootItem.PSIsContainer -and $parent -is [IO.DirectoryInfo] -and !$parent.PSIsContainer) 'The fixture did not reproduce the old parent check'
    Assert (($parent.Attributes -band [IO.FileAttributes]::Hidden) -and
        !($parent.Attributes -band [IO.FileAttributes]::ReparsePoint)) 'Unexpected AppData attributes'

    Set-Version first
    $raw = '%CUSTOM%;C:\User;;'
    $key = New-FakeKey ExpandString $raw
    $before = Get-PathHash (Read-Path $key)

    $fileRoot = Join-Path $base 'not-a-directory'
    [IO.File]::WriteAllText($fileRoot, 'keep')
    $invalidPath = 'Unsafe installation path: ' + [regex]::Escape($fileRoot)
    Assert-Fails { Invoke-PageTTYSetup $fileRoot $source $launcher $key '' $false } $invalidPath
    Assert ([IO.File]::ReadAllText($fileRoot) -ceq 'keep') 'The invalid root file was changed'
    Expect-Unchanged $key $before $null

    $target = [System.Management.Automation.WildcardPattern]::Escape($appData)
    foreach ($linkType in @('Junction','SymbolicLink')) {
        $link = Join-Path $base $linkType
        try {
            New-Item -Path $link -ItemType $linkType -Target $target -Force | Out-Null
        } catch {
            if ($linkType -ne 'SymbolicLink' -or
                $_.FullyQualifiedErrorId -ne 'NewItemSymbolicLinkElevationRequired,Microsoft.PowerShell.Commands.NewItemCommand') { throw }
            Write-Host 'Directory symlink fixture skipped: Windows requires administrator privileges.'
            continue
        }
        try {
            $invalidPath = 'Unsafe installation path: ' + [regex]::Escape($link) + '$'
            foreach ($linkedRoot in @($link, (Join-Path $link 'Local'))) {
                Assert-Fails { Invoke-PageTTYSetup $linkedRoot $source $launcher $key '' $false } $invalidPath
                Expect-Unchanged $key $before $null
                Assert (![IO.Directory]::Exists((Join-Path $appData 'PageTTY')) -and
                    ![IO.Directory]::Exists((Join-Path $appData 'PageTTY.setup'))) 'A rejected link changed its target'
            }
        } finally { [IO.Directory]::Delete($link) }
    }
    Assert ($key.Writes -eq 0 -and $notifications.Count -eq 0) 'Rejected paths caused a PATH write or notification'

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

    $recordPath = Join-Path $dir '.pagetty-install'
    $validRecord = [IO.File]::ReadAllText($recordPath)
    $licensePath = Join-Path $dir 'LICENSE'
    $license = [IO.File]::ReadAllBytes($licensePath)
    [IO.File]::WriteAllText($licensePath, 'user license replacement')
    $validLicenseHash = (ConvertFrom-Json $validRecord).Files.LICENSE
    $invalidHashes = @('[]', 'null', '""', '42', '{}', '["x","y"]', ('["'+$validLicenseHash+'"]'), ('"'+('G'*64)+'"'),
        ('"'+('0'*63)+'"'), ('"'+('0'*65)+'"'), ('"'+('0'*64)+'\n"'))
    foreach ($invalidHash in $invalidHashes) {
        [IO.File]::WriteAllText($recordPath, ($validRecord -creplace '"LICENSE":"[0-9A-F]{64}"', ('"LICENSE":'+$invalidHash)))
        $snapshot = Snapshot
        $writes = $key.Writes
        Assert-Fails { Install $key $dir } 'Invalid SHA-256.*LICENSE'
        Assert-Fails { Uninstall $key } 'Invalid SHA-256.*LICENSE'
        Expect-Unchanged $key $registered $snapshot
        Assert ($key.Writes -eq $writes) 'Invalid file hash reached registry write'
    }
    [IO.File]::WriteAllBytes($licensePath, $license)

    foreach ($case in @('version-string','root-array','unknown-field','files-array','path-array','kind-array','hash-array','before-null','before-missing')) {
        $entry = ConvertFrom-Json $validRecord
        switch ($case) {
            version-string { $entry.Version = '2' }
            root-array { $entry = @($entry) }
            unknown-field { $entry | Add-Member NoteProperty Extra 0 }
            files-array { $entry.Files = @($entry.Files) }
            path-array { $entry.Path = @() }
            kind-array { $entry.Path.Kind = @('ExpandString') }
            hash-array { $entry.Path.Hash = @($entry.Path.Hash) }
            before-null { $entry.Path.BeforeHash = $null }
            before-missing { $entry.Path.PSObject.Properties.Remove('BeforeHash') }
        }
        Save-Record $recordPath $entry
        $snapshot = Snapshot
        Assert-Fails { Install $key $dir } 'Invalid .*record'
        Assert-Fails { Uninstall $key } 'Invalid .*record'
        Expect-Unchanged $key $registered $snapshot
    }
    [IO.File]::WriteAllText($recordPath, $validRecord)

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

    # A corrupted prior kind cannot delete an existing nonempty or empty PATH.
    foreach ($case in @(@{Kind='String'; Raw='C:\Keep;C:\User'}, @{Kind='String'; Raw=''}, @{Kind='ExpandString'; Raw=''})) {
        $value = $case.Raw
        $key = New-FakeKey $case.Kind $value
        $null = Install $key $value
        $before = Get-PathHash (Read-Path $key)
        $validRecord = [IO.File]::ReadAllText($recordPath)
        $entry = ConvertFrom-Json $validRecord
        $entry.Path.Kind = 'Absent'
        Save-Record $recordPath $entry
        $snapshot = Snapshot
        Assert-Fails { Uninstall $key } 'Inconsistent PATH ownership record'
        Assert-Fails { Install $key $dir } 'Inconsistent PATH ownership record'
        Expect-Unchanged $key $before $snapshot
        Assert ($key.Writes -eq 1) 'Corrupt PATH ownership reached registry write'
        $installedValue = $key.Raw
        $key.Raw += ';C:\Added after installation'
        $manuallyEdited = Get-PathHash (Read-Path $key)
        Assert-Fails { Uninstall $key } 'Inconsistent PATH ownership record'
        Expect-Unchanged $key $manuallyEdited $snapshot
        $key.Raw = $installedValue
        [IO.File]::WriteAllText($recordPath, $validRecord)
        $null = Uninstall $key
        Assert ($key.Raw -ceq $value -and $key.Kind -ceq $case.Kind) 'Existing PATH should be restored'
    }

    $key = New-FakeKey String 'C:\Keep'
    $null = Install $key 'C:\Keep'
    $before = Get-PathHash (Read-Path $key)
    $validRecord = [IO.File]::ReadAllText($recordPath)
    $entry = ConvertFrom-Json $validRecord
    $entry.Path.Kind = 'ExpandString'
    $entry.Path.BeforeHash = Get-PathHash @{ Kind = 'ExpandString'; Raw = 'C:\Keep' }
    Save-Record $recordPath $entry
    $snapshot = Snapshot
    Assert-Fails { Uninstall $key } 'Inconsistent PATH ownership record'
    Expect-Unchanged $key $before $snapshot
    [IO.File]::WriteAllText($recordPath, $validRecord)
    $null = Uninstall $key

    foreach ($kind in @('String', 'Absent')) {
        $key = New-FakeKey $kind ''
        $null = Install $key 'C:\Machine'
        $entry = ConvertFrom-Json ([IO.File]::ReadAllText($recordPath))
        $entry.Version = 1
        $entry.Path.PSObject.Properties.Remove('BeforeHash')
        Save-Record $recordPath $entry
        $null = Install $key $dir
        $null = Uninstall $key
        $expected = if ($kind -eq 'Absent') { $dir } else { '' }
        Assert ($key.Kind -ceq 'String' -and $key.Raw -ceq $expected) 'Legacy record attempted an unsafe delete or failed to restore an empty value'
    }

    $key = New-FakeKey ExpandString '%KEEP%;C:\User'
    $null = Install $key 'C:\Keep;C:\User'
    $priorBytes = [IO.File]::ReadAllBytes($recordPath)
    $priorLicense = [IO.File]::ReadAllBytes($licensePath)
    $beforeNotify = $notifications.Count
    $clear = ${function:Clear-Payload}
    & {
        function Clear-Payload($path) {
            if (![IO.Directory]::Exists($path) -or !$path.EndsWith('\old')) { & $clear $path; return }
            $locked = [IO.File]::Open((Join-Path $path 'LICENSE'), 'Open', 'Read', 'None')
            try { & $clear $path } finally { $locked.Dispose() }
        }
        $output = @(Uninstall $key 3>&1 6>&1)
        $message = ($output | ForEach-Object { [string]$_ }) -join [char]10
        $backup = Join-Path $work 'old'
        Assert ($output[-1] -eq 2 -and $message -match 'Uninstall applied.*Cleanup failed') 'Cleanup failure status or message was misleading'
        Assert ($message.Contains($work) -and $message.Contains($backup) -and $message -notmatch 'PageTTY removed') 'Missing precise cleanup warning'
        Assert (![IO.Directory]::Exists($dir)) 'Committed uninstall was rolled back'
        Assert ($key.Raw -ceq '%KEEP%;C:\User' -and $key.Kind -ceq 'ExpandString') 'PATH was not restored'
        Assert ($notifications.Count -eq ($beforeNotify + 1) -and $notifications[-1]) 'Cleanup failure skipped the PATH notification'
        Assert (![IO.File]::Exists((Join-Path $backup 'pagetty.exe'))) 'Cleanup failure was not after deleting the first file'
        Assert ([Convert]::ToBase64String([IO.File]::ReadAllBytes((Join-Path $backup 'LICENSE'))) -ceq [Convert]::ToBase64String($priorLicense)) 'Locked file was not preserved'
        Assert ([Convert]::ToBase64String([IO.File]::ReadAllBytes((Join-Path $backup '.pagetty-install'))) -ceq [Convert]::ToBase64String($priorBytes)) 'Cleanup continued and changed the record'
        Assert ([IO.File]::Exists((Join-Path $backup 'THIRD_PARTY_NOTICES.txt'))) 'Cleanup continued past the failure'
    }
    # Discard the isolated backup left by the simulated failure before the next case.
    [IO.Directory]::Delete($work, $true)

    $key = New-FakeKey String ('C:\Existing;' + $dir)
    $before = Get-PathHash (Read-Path $key)
    $null = Install $key $key.Raw
    $null = Uninstall $key
    Expect-Unchanged $key $before $null
    Assert (!$key.Writes) 'A preexisting PATH entry became owned'
    foreach ($present in @($true, $false)) {
        # A missing key may be created on write; an existing key is reused.
        $key = New-FakeKey Absent '' $present
        $before = Get-PathHash (Read-Path $key)
        $null = Install $key 'C:\Machine'
        $creates = if ($present) { 0 } else { 1 }
        Assert ($key.Present -and $key.Creates -eq $creates -and $key.Kind -ceq 'String' -and $key.Raw -ceq $dir) 'Missing PATH was not installed as REG_SZ'
        $null = Uninstall $key
        Expect-Unchanged $key $before $null
        Assert ($key.Present -and $key.Creates -eq $creates -and $key.Writes -eq 2) 'Uninstall should remove only the Path value'

        $key = New-FakeKey String 'C:\Keep'
        $null = Install $key 'C:\Machine;C:\Keep'
        # An external edit removed the Path value or its entire key.
        $key.Kind = 'Absent'; $key.Raw = ''; $key.Present = $present
        $before = Get-PathHash (Read-Path $key)
        $validRecord = [IO.File]::ReadAllText($recordPath)
        $entry = ConvertFrom-Json $validRecord
        $entry.Path.Kind = 'Absent'
        Save-Record $recordPath $entry
        $snapshot = Snapshot
        Assert-Fails { Uninstall $key } 'Inconsistent PATH ownership record'
        Expect-Unchanged $key $before $snapshot
        [IO.File]::WriteAllText($recordPath, $validRecord)
        $null = Uninstall $key
        Expect-Unchanged $key $before $null
        Assert ($key.Present -eq $present -and $key.Creates -eq 0 -and $key.Writes -eq 1) 'Uninstall recreated a removed key or Path value'
    }

    $key = New-FakeKey Absent '' $false
    $before = Get-PathHash (Read-Path $key)
    $null = Install $key "C:\Machine;$dir"
    $null = Uninstall $key
    Expect-Unchanged $key $before $null
    Assert (!$key.Present -and !$key.Creates -and !$key.Writes) 'Setup created Environment without needing to write PATH'

    foreach ($deny in @('read','write')) {
        $key = New-FakeKey Absent '' $false
        $key.Denied = $deny
        Assert-Fails { Install $key 'C:\Machine' } 'Injected registry access denial'
        $key.Denied = ''
        Expect-Unchanged $key $before $null
        Assert (!$key.Present -and !$key.Creates -and !$key.Writes) 'Access denied was treated as a missing key'
    }

    $key = New-FakeKey String 'C:\Keep'
    $null = Install $key 'C:\Keep'
    $snapshot = Snapshot
    $before = Get-PathHash (Read-Path $key)
    $writes = $key.Writes
    $holdInfo = [Diagnostics.ProcessStartInfo]::new($env:ComSpec, '/d')
    $holdInfo.WorkingDirectory = $dir
    $holdInfo.UseShellExecute = $false
    $holdInfo.CreateNoWindow = $true
    $holdInfo.RedirectStandardInput = $true
    $holdInfo.RedirectStandardOutput = $true
    $holder = [Diagnostics.Process]::Start($holdInfo)
    try {
        Assert-Fails { Uninstall $key } 'Leave this directory in open terminals'
        Expect-Unchanged $key $before $snapshot
        Assert ($key.Writes -eq $writes) 'Uninstall updated PATH while the caller held the install directory'
    } finally {
        $holder.StandardInput.WriteLine('exit')
        if (!$holder.WaitForExit(10000)) { $holder.Kill(); $holder.WaitForExit() }
        $holder.Dispose()
    }
    $null = Uninstall $key

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
if ([Environment]::CurrentDirectory -ine $env:SystemRoot) { throw "Wrong worker startup directory: $([Environment]::CurrentDirectory)" }
$folder = [IO.Path]::GetDirectoryName($env:PAGETTY_SETUP)
[IO.Directory]::Move($folder, [IO.Path]::Combine([IO.Path]::GetDirectoryName($folder), 'launched'))
Write-Host 'LAUNCHER WORKER OK'
return 0
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
    foreach ($cwd in @($base, $fixture)) {
        # A one-shot launcher may start in the directory that it will then release.
        $info.WorkingDirectory = $cwd
        $proc = [Diagnostics.Process]::Start($info)
        try {
            $stdout = $proc.StandardOutput.ReadToEndAsync()
            $stderr = $proc.StandardError.ReadToEndAsync()
            $proc.StandardInput.WriteLine('')
            $proc.StandardInput.Close()
            if (!$proc.WaitForExit(15000)) { $proc.Kill(); throw 'Batch fixture timed out.' }
            if (!$stdout.Wait(15000) -or !$stderr.Wait(15000)) { throw 'Batch worker timed out.' }
            $output = $stdout.Result + $stderr.Result
            Assert ($proc.ExitCode -eq 0 -and $output -match 'Check the uninstall window for the result' -and
                $output -match 'LAUNCHER WORKER OK' -and [IO.Directory]::Exists($moved)) $output
        } finally { $proc.Dispose() }
        [IO.Directory]::Move($moved, $fixture)
    }
    $info.WorkingDirectory = $base
    # Exercise the synchronous launcher's exit status without running the installer payload.
    $fake = Join-Path $base 'install.cmd'
    $body = [char]10 + "Write-Warning 'Install applied. Cleanup failed.'; return 2"
    [IO.File]::WriteAllText($fake, (($header + $body) -replace '\r?\n', ([string][char]13 + [char]10)))
    $info.Arguments = '/d /c ""' + $fake + '""'
    $proc = [Diagnostics.Process]::Start($info)
    try {
        $proc.StandardInput.WriteLine('')
        $proc.StandardInput.Close()
        if (!$proc.WaitForExit(15000)) { $proc.Kill(); throw 'Batch exit-code fixture timed out.' }
        $output = $proc.StandardOutput.ReadToEnd() + $proc.StandardError.ReadToEnd()
        Assert ($proc.ExitCode -eq 2 -and $output -match 'Cleanup failed' -and $output -notmatch 'PageTTY setup failed') $output
    } finally { $proc.Dispose() }
    Write-Host 'Windows installer tests passed with temporary files, a fake registry and batch fixtures.'

} finally {
    [IO.Directory]::Delete($base, $true)
}
