# cleanup-smartcore.ps1 — wipe every trace of older Smartcore installs.
#
# Run me from PowerShell. The script self-elevates via UAC if needed
# (admin rights are required to remove the Windows service and to
# delete %ProgramFiles%\Smartcore + %ProgramData%\Smartcore).
#
# What gets removed:
#
#   1. Smartcore Windows service (sc delete Smartcore)
#   2. HKCU\…\Run\Smartcore (old run-key persistence)
#   3. HKLM\…\Run\Smartcore
#   4. Scheduled tasks named *Smartcore* / *Core Agent*
#   5. %LOCALAPPDATA%\Smartcore         — per-user installer cache (AI + video bytes)
#   6. %LOCALAPPDATA%\WorkTrack          — legacy folder name
#   7. %LOCALAPPDATA%\Smart Auto Mission LLC — AI agent's own user data
#   8. %ProgramData%\Smartcore           — system-wide config from old service install
#   9. %ProgramFiles%\Smartcore          — system-wide install dir from old service install
#  10. Smartcore.exe / setup.exe in %USERPROFILE%\Downloads (older builds)
#
# After this script, the machine is in the same state as a fresh
# Win11 install w.r.t. Smartcore. Re-running Smartcore.exe (the new
# 1.0 single-shot installer) will re-create everything user-scope.

$ErrorActionPreference = 'Continue'

# Self-elevate if not admin.
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-Object Security.Principal.WindowsPrincipal($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Host "Need admin. Re-launching with UAC prompt..."
    Start-Process powershell -Verb RunAs -ArgumentList "-NoProfile","-ExecutionPolicy","Bypass","-File","`"$PSCommandPath`""
    exit
}

Write-Host "============================================================"
Write-Host "  Smartcore cleanup (running elevated)"
Write-Host "============================================================"
Write-Host ""

# ----- 1. Stop + delete Windows service -----
$svc = Get-Service -Name 'Smartcore' -ErrorAction SilentlyContinue
if ($svc) {
    Write-Host "[1] Stopping + deleting Windows service 'Smartcore'..."
    if ($svc.Status -ne 'Stopped') {
        Stop-Service -Name 'Smartcore' -Force -ErrorAction SilentlyContinue
        # Wait briefly for the SCM to release the binary handle.
        for ($i = 0; $i -lt 20; $i++) {
            $s = Get-Service -Name 'Smartcore' -ErrorAction SilentlyContinue
            if (-not $s -or $s.Status -eq 'Stopped') { break }
            Start-Sleep -Milliseconds 250
        }
    }
    & sc.exe delete Smartcore | Out-Null
    Write-Host "    OK service removed"
} else {
    Write-Host "[1] Smartcore service: not present"
}
Write-Host ""

# ----- 2-3. Run-key persistence (both hives) -----
foreach ($hive in @('HKCU','HKLM')) {
    $key = "${hive}:\Software\Microsoft\Windows\CurrentVersion\Run"
    foreach ($name in @('Smartcore','SmartcoreWatchdog','WorkTrackAgent','WorkTrackWatchdog')) {
        try {
            $v = (Get-ItemProperty -Path $key -ErrorAction Stop).$name
        } catch { $v = $null }
        if ($v) {
            Remove-ItemProperty -Path $key -Name $name -ErrorAction SilentlyContinue
            Write-Host "[2] Removed ${hive}\…\Run\${name}"
        }
    }
}

# ----- 4. Scheduled tasks -----
$tasks = Get-ScheduledTask -ErrorAction SilentlyContinue | Where-Object {
    $_.TaskName -imatch 'martcore|core.?agent|orktrack|S\.A\.M' -or
    ($_.Actions | Where-Object { $_.Execute -imatch 'martcore|core.?agent|orktrack|S\.A\.M' })
}
if ($tasks) {
    foreach ($t in $tasks) {
        Write-Host ("[4] Removing scheduled task {0}{1}" -f $t.TaskPath, $t.TaskName)
        Unregister-ScheduledTask -TaskName $t.TaskName -TaskPath $t.TaskPath -Confirm:$false -ErrorAction SilentlyContinue
    }
} else {
    Write-Host "[4] Scheduled tasks: none matched"
}
Write-Host ""

# ----- 5-9. Filesystem cleanup -----
$paths = @(
    "$env:LOCALAPPDATA\Smartcore",
    "$env:LOCALAPPDATA\WorkTrack",
    "$env:LOCALAPPDATA\Smart Auto Mission LLC",
    "$env:ProgramData\Smartcore",
    "$env:ProgramFiles\Smartcore",
    "${env:ProgramFiles(x86)}\Smartcore"
)
foreach ($p in $paths) {
    if ($p -and (Test-Path $p)) {
        try {
            $sz = (Get-ChildItem $p -Recurse -Force -ErrorAction SilentlyContinue | Measure-Object Length -Sum).Sum
        } catch { $sz = 0 }
        Write-Host ("[5] Removing {0}  ({1:N0} bytes)" -f $p, $sz)
        Remove-Item -Path $p -Recurse -Force -ErrorAction SilentlyContinue
    }
}

# Stale Smartcore.exe / setup.exe in Downloads (older builds).
$dl = "$env:USERPROFILE\Downloads"
if (Test-Path $dl) {
    foreach ($pattern in @('Smartcore*.exe','setup*.exe','Setup*.exe')) {
        Get-ChildItem $dl -Filter $pattern -ErrorAction SilentlyContinue | ForEach-Object {
            Write-Host ("[6] Removing {0}  ({1:N0} bytes)" -f $_.FullName, $_.Length)
            Remove-Item -Path $_.FullName -Force -ErrorAction SilentlyContinue
        }
    }
}

Write-Host ""
Write-Host "============================================================"
Write-Host "  Verification"
Write-Host "============================================================"
$remaining = @()
if (Get-Service -Name 'Smartcore' -ErrorAction SilentlyContinue) { $remaining += "Service Smartcore" }
foreach ($hive in @('HKCU','HKLM')) {
    $key = "${hive}:\Software\Microsoft\Windows\CurrentVersion\Run"
    if ((Get-ItemProperty -Path $key -ErrorAction SilentlyContinue).Smartcore) {
        $remaining += "$hive Run\Smartcore"
    }
}
foreach ($p in $paths) {
    if ($p -and (Test-Path $p)) { $remaining += $p }
}

if ($remaining.Count -eq 0) {
    Write-Host "All clear ✓"
} else {
    Write-Host "Still present (re-run as admin or check locks):"
    foreach ($r in $remaining) { Write-Host "  - $r" }
}

Write-Host ""
Write-Host "Done. Press any key to close..."
$null = $host.UI.RawUI.ReadKey('NoEcho,IncludeKeyDown')
