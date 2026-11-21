@echo off
setlocal DisableDelayedExpansion
set "PAGETTY_SOURCE=%~dp0"
set "PAGETTY_MODE=%~1"
"%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -NonInteractive -Command ^
  "$ErrorActionPreference = 'Stop';" ^
  "function Check-File($file) {" ^
  "  $item = Get-Item -LiteralPath $file -Force -ErrorAction SilentlyContinue;" ^
  "  if ($item -and ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint))) { throw ('Unexpected file: ' + $file) }" ^
  "}" ^
  "function Save-State($first, $last, $pathMode) {" ^
  "  [IO.File]::WriteAllLines($recordStage, [string[]]@($first, $last, $pathMode));" ^
  "  if ([IO.File]::Exists($record)) { [IO.File]::Replace($recordStage, $record, [NullString]::Value) }" ^
  "  else { [IO.File]::Move($recordStage, $record) }" ^
  "}" ^
  "function Notify-PathChange {" ^
  "  try {" ^
  "    $q = [char]34;" ^
  "    $signature = '[DllImport(' + $q + 'user32.dll' + $q + ', CharSet=CharSet.Unicode)] public static extern IntPtr SendMessageTimeout(IntPtr hwnd, uint msg, UIntPtr w, string l, uint flags, uint ms, out UIntPtr result);';" ^
  "    Add-Type -Namespace PageTTY -Name Native -MemberDefinition $signature;" ^
  "    $result = [UIntPtr]::Zero;" ^
  "    $sent = [PageTTY.Native]::SendMessageTimeout([IntPtr]0xffff, 0x1a, [UIntPtr]::Zero, 'Environment', 2, 1000, [ref]$result);" ^
  "    if ($sent -eq [IntPtr]::Zero) { Write-Warning 'If PATH is stale, sign out and back in.' }" ^
  "  } catch { Write-Warning 'If PATH is stale, sign out and back in.' }" ^
  "}" ^
  "try {" ^
  "  $mode = $env:PAGETTY_MODE;" ^
  "  if ($mode -and $mode -ne 'uninstall') { throw 'Use: install.cmd [uninstall]' }" ^
  "  $local = [Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData);" ^
  "  if (!$local -or $local.Contains(';') -or $local.Contains([char]37)) { throw 'Unsupported LocalAppData path.' }" ^
  "  $dir = Join-Path $local 'PageTTY';" ^
  "  $bin = Join-Path $dir 'pagetty.exe';" ^
  "  $record = Join-Path $dir '.pagetty-install';" ^
  "  $docs = @('LICENSE', 'THIRD_PARTY_NOTICES.txt');" ^
  "  $first = 'pending'; $pathMode = 'none';" ^
  "  if ($mode -ne 'uninstall') {" ^
  "    $src = Join-Path $env:PAGETTY_SOURCE 'pagetty.exe';" ^
  "    foreach ($file in @('pagetty.exe') + $docs) {" ^
  "      if (![IO.File]::Exists((Join-Path $env:PAGETTY_SOURCE $file))) { throw 'Extract the Windows ZIP before running install.cmd.' }" ^
  "    }" ^
  "  }" ^
  "  $item = Get-Item -LiteralPath $dir -Force -ErrorAction SilentlyContinue;" ^
  "  if ($item) {" ^
  "    if (!$item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -or ![IO.File]::Exists($record)) { throw ('Installation directory already exists and is not managed: ' + $dir) }" ^
  "    Check-File $record; Check-File $bin;" ^
  "    $saved = [IO.File]::ReadAllLines($record);" ^
  "    if ($saved.Length -ne 3 -or $saved[0] -notmatch '^(pending|[0-9a-f]{64})$' -or $saved[1] -notmatch '^[0-9a-f]{64}$' -or $saved[2] -notin @('none','absent','empty','append')) { throw 'Invalid installation record.' }" ^
  "    $pathMode = $saved[2];" ^
  "    if ([IO.File]::Exists($bin)) {" ^
  "      $first = (Get-FileHash -LiteralPath $bin -Algorithm SHA256).Hash;" ^
  "      if ($first -notin $saved[0..1]) { throw ('Existing executable was not installed by this installer: ' + $bin) }" ^
  "    }" ^
  "    foreach ($doc in $docs) { Check-File (Join-Path $dir $doc) }" ^
  "  } elseif ($mode -eq 'uninstall') { Write-Host 'PageTTY is not installed.'; exit 0 }" ^
  "  $reg = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment');" ^
  "  $hadPath = $reg.GetValueNames() -contains 'Path';" ^
  "  $kind = [Microsoft.Win32.RegistryValueKind]::String;" ^
  "  $raw = '';" ^
  "  if ($hadPath) {" ^
  "    $kind = $reg.GetValueKind('Path');" ^
  "    if ($kind -notin @('String','ExpandString')) { throw 'The user Path has an unsupported registry type.' }" ^
  "    $raw = $reg.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames);" ^
  "  }" ^
  "  $entries = $raw.Split(';');" ^
  "  $changed = $false;" ^
  "  if ($mode -eq 'uninstall') {" ^
  "    if ($pathMode -ne 'none') {" ^
  "      for ($i = $entries.Length - 1; $i -ge 0; $i--) {" ^
  "        if ($entries[$i] -cne $dir) { continue }" ^
  "        $newPath = (@(for ($j = 0; $j -lt $entries.Length; $j++) { if ($j -ne $i) { $entries[$j] } }) -join ';');" ^
  "        if ($entries.Length -eq 1 -and $pathMode -eq 'absent') { $reg.DeleteValue('Path') }" ^
  "        else { $reg.SetValue('Path', $newPath, $kind) }" ^
  "        $changed = $true; break;" ^
  "      }" ^
  "    }" ^
  "    foreach ($file in @($bin) + @($docs | ForEach-Object { Join-Path $dir $_ })) { [IO.File]::Delete($file) }" ^
  "    [IO.File]::Delete($record);" ^
  "    if (![IO.Directory]::GetFileSystemEntries($dir).Length) { [IO.Directory]::Delete($dir) }" ^
  "    Write-Host 'PageTTY removed.';" ^
  "  } else {" ^
  "    $existing = $raw;" ^
  "    if ($kind -eq 'ExpandString') { $existing = [Environment]::ExpandEnvironmentVariables($raw) }" ^
  "    $machine = [Environment]::GetEnvironmentVariable('Path', 'Machine');" ^
  "    if ($machine) { $existing += ';' + $machine }" ^
  "    $matched = $false;" ^
  "    foreach ($entry in $existing.Split(';')) {" ^
  "      $candidate = $entry.Trim().Trim([char]34).Replace('/', '\').TrimEnd('\');" ^
  "      if ($candidate -ieq $dir) { $matched = $true; break }" ^
  "    }" ^
  "    if (!$matched) {" ^
  "      $pathMode = if (!$hadPath) { 'absent' } elseif (!$raw) { 'empty' } else { 'append' };" ^
  "      $newPath = if ($raw) { $raw + ';' + $dir } else { $dir };" ^
  "      if ($newPath.Length -ge 32767) { throw 'The resulting user Path is too long.' }" ^
  "    }" ^
  "    $last = (Get-FileHash -LiteralPath $src -Algorithm SHA256).Hash;" ^
  "    if (!$item) { [IO.Directory]::CreateDirectory($dir) | Out-Null }" ^
  "    $stage = Join-Path $dir ([IO.Path]::GetRandomFileName());" ^
  "    $recordStage = Join-Path $dir ([IO.Path]::GetRandomFileName());" ^
  "    try {" ^
  "      Save-State $first $last $pathMode;" ^
  "      [IO.File]::Copy($src, $stage);" ^
  "      if ([IO.File]::Exists($bin)) { [IO.File]::Replace($stage, $bin, [NullString]::Value) }" ^
  "      else { [IO.File]::Move($stage, $bin) }" ^
  "      foreach ($doc in $docs) { [IO.File]::Copy((Join-Path $env:PAGETTY_SOURCE $doc), (Join-Path $dir $doc), $true) }" ^
  "    } finally { [IO.File]::Delete($stage); [IO.File]::Delete($recordStage) }" ^
  "    if (!$matched) { $reg.SetValue('Path', $newPath, $kind); $changed = $true }" ^
  "    Write-Host ('Installed: ' + $bin);" ^
  "  }" ^
  "  $reg.Dispose();" ^
  "  if ($changed) { Notify-PathChange }" ^
  "  Write-Host 'Close and reopen your terminal. If PATH is stale, sign out and back in.';" ^
  "  if ($mode -ne 'uninstall') { Write-Host 'Try: pagetty --help' }" ^
  "} catch { [Console]::Error.WriteLine($_.Exception.Message); exit 1 }"
if errorlevel 1 goto failed
pause
exit /b 0
:failed
echo PageTTY setup failed.
pause
exit /b 1
