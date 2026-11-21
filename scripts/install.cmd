@echo off
setlocal DisableDelayedExpansion
set "PAGETTY_SETUP=%~f0"
set "PAGETTY_UNINSTALL="
if /i "%~nx0"=="uninstall.cmd" set "PAGETTY_UNINSTALL=1"
set "PAGETTY_COMMAND=$status = 0; try { $text = [IO.File]::ReadAllText($env:PAGETTY_SETUP); $marker = [char]10 + ':payload'; $offset = $text.IndexOf($marker); if ($offset -lt 0) { throw 'Setup payload is missing.' }; & ([scriptblock]::Create($text.Substring($offset + $marker.Length))) } catch { [Console]::Error.WriteLine($_.Exception.Message); $status = 1 }; if ($env:PAGETTY_UNINSTALL -eq '1') { try { $null = Read-Host 'Press Enter to close' } catch {} }; exit $status"
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

function Get-PathPlan($before, [string]$effective, $dir, $ownership, [bool]$uninstall) {
    $plan = @{ Value = $before; Change = $false; Ownership = $ownership; Manual = $false }
    if ($uninstall) {
        if (!$ownership) { return $plan }
        $entries = $before.Raw.Split(';')
        $matches = @($entries | Where-Object { $_ -ieq $dir })
        if ($ownership.Hash -cne (Get-PathHash $before) -or $matches.Count -ne 1 -or $entries[-1] -cne $dir) {
            $plan.Manual = $true
            return $plan
        }
        $previous = $before.Raw.Substring(0, [Math]::Max(0, $before.Raw.Length - $dir.Length - 1))
        $plan.Value = @{ Kind = $ownership.Kind; Raw = $previous }
    } else {
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
        $plan.Ownership = @{ Kind = $before.Kind; Hash = (Get-PathHash $plan.Value) }
    }
    $plan.Change = $true
    $plan
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
    $state = [IO.File]::ReadAllText($path) | ConvertFrom-Json
    if ($state.Version -ne 1 -or @($state.Files.PSObject.Properties).Count -ne $payload.Count) {
        throw "Unknown installation record: $path"
    }
    foreach ($name in $payload) {
        $path = Join-Path $dir $name
        Assert-PlainFile $path
        $expected = $state.Files.PSObject.Properties[$name]
        if (!$expected -or $expected.Value -cne (Get-FileHash -LiteralPath $path).Hash) {
            throw "Managed file has changed: $path"
        }
    }
    if ($state.Path -and ($state.Path.Kind -notin @('Absent','String','ExpandString') -or
        $state.Path.Hash -cnotmatch '^[0-9A-F]{64}$')) { throw 'Invalid PATH ownership record.' }
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
    try {
        $state = Get-InstalledState $dir
        if (!$state -and $uninstall) { Write-Host 'PageTTY is not installed.'; return }
        $before = Read-Path $key
        if (!$uninstall -and $null -eq $effective) { $effective = Read-EffectivePath }
        $plan = Get-PathPlan $before $effective $dir $state.Path $uninstall
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
            Save-Record (Join-Path $stage $record) @{ Version = 1; Files = $hashes; Path = $plan.Ownership }
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
        if (!$keepWork) {
            try {
                Clear-Payload $stage
                if ($movedOld -and [IO.Directory]::Exists($backup)) { $null = Get-InstalledState $backup }
                Clear-Payload $backup
                [IO.Directory]::Delete($work)
            } catch { throw "Could not remove setup files in $work. Inspect before retrying. $($_.Exception.Message)" }
        }
    }
    if ($plan.Manual) {
        Write-Warning "User PATH changed since setup and was left untouched. In user Environment Variables, review the PageTTY entry: $dir"
    }
    if ($uninstall) { Write-Host 'PageTTY removed.' }
    else { Write-Host "Installed: $dir\pagetty.exe"; Write-Host 'Try: pagetty --help' }
    return $plan.Change
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
    $changed = Invoke-PageTTYSetup $root ([IO.Path]::GetDirectoryName($env:PAGETTY_SETUP)) $env:PAGETTY_SETUP $key $null $uninstall
} finally { $key.Dispose() }
if ($changed) {
    try {
        if ($uninstall) { Initialize-Native }
        $result = [UIntPtr]::Zero
        $sent = [PageTTY.Native]::SendMessageTimeout([IntPtr]0xffff, 0x1a, [UIntPtr]::Zero, 'Environment', 2, 1000, [ref]$result)
        if ($sent -eq [IntPtr]::Zero) { Write-Warning 'Windows did not confirm the PATH refresh.' }
    } catch { Write-Warning 'Windows did not confirm the PATH refresh.' }
}
Write-Host 'Close and reopen your terminal. If PATH is stale, sign out and back in.'
