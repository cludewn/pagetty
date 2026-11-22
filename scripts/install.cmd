@echo off
setlocal DisableDelayedExpansion
set "PAGETTY_SETUP=%~f0"
set "PAGETTY_UNINSTALL="
if /i "%~nx0"=="uninstall.cmd" set "PAGETTY_UNINSTALL=1"
set "PAGETTY_COMMAND=$status = 0; try { $text = [IO.File]::ReadAllText($env:PAGETTY_SETUP); $marker = [char]10 + ':payload'; $offset = $text.IndexOf($marker); if ($offset -lt 0) { throw 'Setup payload is missing.' }; $status = & ([scriptblock]::Create($text.Substring($offset + $marker.Length))) } catch { [Console]::Error.WriteLine($_.Exception.Message); $status = 1 }; if ($env:PAGETTY_UNINSTALL -eq '1') { try { $null = Read-Host 'Press Enter to close' } catch {} }; exit $status"
if defined PAGETTY_UNINSTALL (
    start "PageTTY uninstall" /d "%SystemRoot%" "%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -Command "%PAGETTY_COMMAND%"
    if errorlevel 1 (
        echo Could not open the PageTTY uninstall window.
        pause
        exit /b 1
    )
    exit /b 0
)
"%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -NonInteractive -Command "%PAGETTY_COMMAND%"
if errorlevel 2 (
    pause
    exit /b 2
)
if errorlevel 1 (
    echo PageTTY setup failed.
    pause
    exit /b 1
)
pause
exit /b 0
:payload
$ErrorActionPreference = 'Stop'
$payload = @('pagetty.exe', 'LICENSE', 'THIRD_PARTY_NOTICES.txt', 'uninstall.cmd')
$record = '.pagetty-install'

function Get-Entry($path) {
    try { Get-Item -LiteralPath $path -Force -ErrorAction Stop }
    catch [System.Management.Automation.ItemNotFoundException] {}
}

function Assert-PlainFile($path) {
    $item = Get-Entry $path
    if (!$item -or $item.PSIsContainer -or ($item.Attributes -band ([IO.FileAttributes]::ReparsePoint -bor [IO.FileAttributes]::ReadOnly))) {
        throw "Missing or unsafe file: $path"
    }
}

function Read-Path($key) {
    if ($key.GetValueNames() -notcontains 'Path') { return @{ Kind = 'Absent'; Raw = '' } }
    $kind = [string]$key.GetValueKind('Path')
    if ($kind -notin @('String','ExpandString')) { throw 'Unsupported user Path registry type.' }
    @{ Kind = $kind; Raw = $key.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames) }
}

function Write-Path($key, $value) {
    if ($value.Kind -eq 'Absent') { $key.DeleteValue('Path', $false) }
    else { $key.SetValue('Path', $value.Raw, [Microsoft.Win32.RegistryValueKind]$value.Kind) }
}

function Get-PathHash($value) {
    $sha = [Security.Cryptography.SHA256]::Create()
    try {
        $bytes = [Text.Encoding]::Unicode.GetBytes($value.Kind + [char]10 + $value.Raw)
        [BitConverter]::ToString($sha.ComputeHash($bytes)).Replace('-', '')
    } finally { $sha.Dispose() }
}

function Get-PathPlan($before, [string]$effective, $dir, $ownership, [bool]$uninstall, [int]$version = 2) {
    $plan = @{ Value = $before; Change = $false; Ownership = $ownership; Manual = $false; Version = $version }
    if ($ownership) {
        if ($ownership.Kind -ceq 'Absent' -and
            ($ownership.Hash -ine (Get-PathHash @{ Kind = 'String'; Raw = $dir }) -or
             ($version -eq 2 -and $ownership.BeforeHash -ine (Get-PathHash @{ Kind = 'Absent'; Raw = '' })))) {
            throw 'Inconsistent PATH ownership record.'
        }
        if ($ownership.Hash -ine (Get-PathHash $before)) {
            $plan.Manual = $true
            return $plan
        }
        if ($before.Kind -eq 'Absent') { throw 'Inconsistent PATH ownership record.' }
        $entries = $before.Raw.Split(';')
        $matches = @($entries | Where-Object { $_ -ieq $dir })
        if ($matches.Count -ne 1 -or $entries[-1] -cne $dir) {
            throw 'Inconsistent PATH ownership record.'
        }
        $previous = $before.Raw.Substring(0, [Math]::Max(0, $before.Raw.Length - $dir.Length - 1))
        $prior = @{ Kind = $ownership.Kind; Raw = $previous }
        $expectedKind = if ($prior.Kind -ceq 'Absent') { 'String' } else { $prior.Kind }
        if ($before.Kind -cne $expectedKind -or
            (!$previous -and $before.Raw -cne $dir) -or
            ($prior.Kind -ceq 'Absent' -and $before.Raw -cne $dir) -or
            ($version -eq 2 -and $ownership.BeforeHash -ine (Get-PathHash $prior))) {
            throw 'Inconsistent PATH ownership record.'
        }
        if ($uninstall) {
            # The old format cannot distinguish an absent PATH from an empty REG_SZ.
            if ($version -eq 1 -and $prior.Kind -ceq 'Absent') {
                $plan.Manual = $true
                return $plan
            }
            $plan.Value = $prior
            $plan.Change = $true
        }
        return $plan
    }
    if (!$uninstall) {
        foreach ($entry in $effective.Split(';') + $before.Raw.Split(';')) {
            $candidate = $entry.Trim().Trim([char]34).Replace('/', '\').TrimEnd('\')
            if ($candidate -ieq $dir) { return $plan }
        }
        $combined = if ($effective) { "$effective;$dir" } else { $dir }
        if ($combined.Length -gt 8191) {
            throw "The expanded system + user PATH would exceed cmd.exe's 8191-character limit. PATH was not changed."
        }
        $raw = if ($before.Raw) { $before.Raw + ';' + $dir } else { $dir }
        if ($raw.Length -gt 32767) { throw 'The user Path would exceed the Windows environment limit.' }
        $kind = if ($before.Kind -eq 'Absent') { 'String' } else { $before.Kind }
        $plan.Value = @{ Kind = $kind; Raw = $raw }
        $plan.Ownership = @{ Kind = $before.Kind; Hash = (Get-PathHash $plan.Value); BeforeHash = (Get-PathHash $before) }
        $plan.Version = 2
        $plan.Change = $true
    }
    $plan
}

function Assert-RecordFields($value, [string[]]$fields, $message) {
    if ($value -isnot [System.Management.Automation.PSCustomObject]) { throw $message }
    $names = @($value.PSObject.Properties | ForEach-Object Name)
    if ($names.Count -ne $fields.Count) { throw $message }
    foreach ($name in $fields) {
        if ($names -cnotcontains $name) { throw $message }
    }
}

function Test-Sha256($value) {
    $value -is [string] -and $value -cmatch '\A[0-9A-Fa-f]{64}\z'
}

function Get-InstalledState($dir) {
    $item = Get-Entry $dir
    if (!$item) { return }
    if (!$item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
        throw "Unsafe installation directory: $dir"
    }
    if ([IO.Directory]::GetFileSystemEntries($dir).Length -ne $payload.Count + 1) {
        throw "Unexpected files in $dir"
    }
    $path = Join-Path $dir $record
    Assert-PlainFile $path
    $json = [IO.File]::ReadAllText($path)
    $invalid = "Invalid installation record: $path"
    if ($json -notmatch '\A\s*\{') { throw $invalid }
    $state = ConvertFrom-Json -InputObject $json
    Assert-RecordFields $state @('Version','Files','Path') $invalid
    if (($state.Version -isnot [int] -and $state.Version -isnot [long]) -or $state.Version -notin @(1,2)) {
        throw $invalid
    }
    Assert-RecordFields $state.Files $payload $invalid
    if ($null -ne $state.Path) {
        $fields = @('Kind','Hash')
        if ($state.Version -eq 2) { $fields += 'BeforeHash' }
        Assert-RecordFields $state.Path $fields 'Invalid PATH ownership record.'
        if ($state.Path.Kind -isnot [string] -or $state.Path.Kind -cnotin @('Absent','String','ExpandString') -or
            !(Test-Sha256 $state.Path.Hash) -or ($state.Version -eq 2 -and !(Test-Sha256 $state.Path.BeforeHash))) {
            throw 'Invalid PATH ownership record.'
        }
    }
    foreach ($name in $payload) {
        $path = Join-Path $dir $name
        $expected = $state.Files.PSObject.Properties[$name].Value
        if (!(Test-Sha256 $expected)) { throw "Invalid SHA-256 in installation record for $name." }
        Assert-PlainFile $path
        if ($expected -ine (Get-FileHash -LiteralPath $path).Hash) {
            throw "Managed file has changed: $path"
        }
    }
    $state
}

function Copy-PayloadFile($from, $to) { [IO.File]::Copy($from, $to) }
function Save-Record($file, $state) {
    [IO.File]::WriteAllText($file, (ConvertTo-Json -InputObject $state -Depth 4 -Compress))
}
function Move-Payload($from, $to) { [IO.Directory]::Move($from, $to) }
function Clear-Payload($path) {
    if (![IO.Directory]::Exists($path)) { return }
    foreach ($name in $payload + $record) { [IO.File]::Delete((Join-Path $path $name)) }
    [IO.Directory]::Delete($path)
}

function Invoke-PageTTYSetup($root, $source, $launcher, $key, $effective, [bool]$uninstall) {
    $dir = Join-Path $root 'PageTTY'
    $work = Join-Path $root 'PageTTY.setup'
    $stage = Join-Path $work 'new'
    $backup = Join-Path $work 'old'
    if (Get-Entry $work) { throw "Another setup is active or recovery is needed: $work" }
    New-Item -Path $work -ItemType Directory -ErrorAction Stop | Out-Null
    $keepWork = $false
    $movedOld = $false
    $placedNew = $false
    $attemptedPath = $false
    $committed = $false
    $cleanupError = $null
    try {
        $state = Get-InstalledState $dir
        if (!$state -and $uninstall) { Write-Host 'PageTTY is not installed.'; return 0 }
        $before = Read-Path $key
        if (!$uninstall -and $null -eq $effective) { $effective = Read-EffectivePath }
        $version = if ($state) { $state.Version } else { 2 }
        $plan = Get-PathPlan $before $effective $dir $state.Path $uninstall $version
        if (!$uninstall) {
            $sources = @{}
            foreach ($name in $payload) {
                $file = if ($name -eq 'uninstall.cmd') { $launcher } else { Join-Path $source $name }
                Assert-PlainFile $file
                $sources[$name] = $file
            }
            [IO.Directory]::CreateDirectory($stage) | Out-Null
            $hashes = @{}
            foreach ($name in $payload) {
                $target = Join-Path $stage $name
                Copy-PayloadFile $sources[$name] $target
                $hashes[$name] = (Get-FileHash -LiteralPath $target).Hash
            }
            Save-Record (Join-Path $stage $record) @{ Version = $plan.Version; Files = $hashes; Path = $plan.Ownership }
        }
        if ($state) {
            $null = Get-InstalledState $dir
            Move-Payload $dir $backup
            $movedOld = $true
        }
        if (!$uninstall) {
            Move-Payload $stage $dir
            $placedNew = $true
        }
        if ($plan.Change) {
            if ((Get-PathHash (Read-Path $key)) -cne (Get-PathHash $before)) {
                throw 'User PATH changed during setup. Retry the operation.'
            }
            $attemptedPath = $true
            Write-Path $key $plan.Value
        }
        $committed = $true
    } catch {
        $failure = $_
        $restoreErrors = @()
        if ($attemptedPath) {
            try {
                $current = Get-PathHash (Read-Path $key)
                if ($current -ceq (Get-PathHash $plan.Value)) { Write-Path $key $before }
                elseif ($current -cne (Get-PathHash $before)) { throw 'PATH changed again and was left untouched.' }
            } catch { $restoreErrors += "PATH: $($_.Exception.Message)" }
        }
        try {
            if ($placedNew) { Move-Payload $dir $stage }
            if ($movedOld) { Move-Payload $backup $dir }
        } catch { $restoreErrors += "Files: $($_.Exception.Message)" }
        if ($restoreErrors.Count) {
            $keepWork = $true
            throw "$($failure.Exception.Message) Rollback incomplete. Recovery directory: $work. $($restoreErrors -join ' ')"
        }
        throw $failure
    } finally {
        if ($committed -and $plan.Change) { Send-PathNotification $uninstall }
        if (!$keepWork) {
            try {
                Clear-Payload $stage
                if ($movedOld -and [IO.Directory]::Exists($backup)) { $null = Get-InstalledState $backup }
                Clear-Payload $backup
                [IO.Directory]::Delete($work)
            } catch {
                $cleanupError = "Cleanup failed. Remaining setup files: $work. $($_.Exception.Message)"
                if ([IO.Directory]::Exists($backup)) { $cleanupError += " Retired installation: $backup." }
                if (!$committed) { throw $cleanupError }
            }
        }
    }
    if ($plan.Manual) {
        Write-Warning "User PATH ownership could not be confirmed. PATH was left untouched. In user Environment Variables, review the PageTTY entry: $dir"
    }
    if ($cleanupError) {
        $operation = if ($uninstall) { 'Uninstall' } else { 'Install' }
        Write-Warning "$operation applied. $cleanupError"
        return 2
    }
    if ($uninstall) { Write-Host 'PageTTY removed.' }
    else { Write-Host "Installed: $dir\pagetty.exe"; Write-Host 'Try: pagetty --help' }
    return 0
}

function Initialize-Native {
    Add-Type -Namespace PageTTY -Name Native -MemberDefinition @(
        '[DllImport("userenv.dll", SetLastError=true)] public static extern bool CreateEnvironmentBlock(out IntPtr block, IntPtr token, bool inherit);'
        '[DllImport("userenv.dll")] public static extern bool DestroyEnvironmentBlock(IntPtr block);'
        '[DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern IntPtr SendMessageTimeout(IntPtr h, uint m, UIntPtr w, string l, uint f, uint ms, out UIntPtr result);'
    )
}

function Read-EffectivePath {
    # The fresh Windows user environment combines and expands the stored paths.
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $block = [IntPtr]::Zero
    try {
        if (![PageTTY.Native]::CreateEnvironmentBlock([ref]$block, $identity.Token, $false)) {
            throw 'Could not read the combined Windows PATH.'
        }
        $p = $block
        while ($line = [Runtime.InteropServices.Marshal]::PtrToStringUni($p)) {
            if ($line.StartsWith('Path=', [StringComparison]::OrdinalIgnoreCase)) { return $line.Substring(5) }
            $p = [IntPtr]::Add($p, ($line.Length + 1) * 2)
        }
        return ''
    } finally {
        if ($block -ne [IntPtr]::Zero) { [void][PageTTY.Native]::DestroyEnvironmentBlock($block) }
        $identity.Dispose()
    }
}

function Send-PathNotification([bool]$uninstall) {
    try {
        if ($uninstall) { Initialize-Native }
        $result = [UIntPtr]::Zero
        $sent = [PageTTY.Native]::SendMessageTimeout([IntPtr]0xffff, 0x1a, [UIntPtr]::Zero, 'Environment', 2, 1000, [ref]$result)
        if ($sent -eq [IntPtr]::Zero) { Write-Warning 'Windows did not confirm the PATH refresh.' }
    } catch { Write-Warning 'Windows did not confirm the PATH refresh.' }
}

# Command entry point
$root = [Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)
if (!$root -or $root -match '[;%]') { throw 'Unsupported LocalAppData location.' }
for ($item = Get-Item -LiteralPath $root -Force; $item; $item = $item.Parent) {
    if (!$item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
        throw "Unsafe installation path: $($item.FullName)"
    }
}
$uninstall = $env:PAGETTY_UNINSTALL -eq '1'
if (!$uninstall) { Initialize-Native }
$key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
if (!$key) { throw 'Cannot open the user Environment registry key for writing.' }
try {
    $status = Invoke-PageTTYSetup $root ([IO.Path]::GetDirectoryName($env:PAGETTY_SETUP)) $env:PAGETTY_SETUP $key $null $uninstall
} finally { $key.Dispose() }
Write-Host 'Close and reopen your terminal. If PATH is stale, sign out and back in.'
return $status
