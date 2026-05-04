# cleanup-admin.ps1 - admin-only cleanup for old Smartcore install.
# Removes Windows service + system folders. Run elevated.

$ErrorActionPreference = 'Continue'

Write-Host "============================================================"
Write-Host "  Smartcore admin cleanup"
Write-Host "============================================================"
Write-Host ""

# 1. Service
$svc = Get-Service -Name 'Smartcore' -ErrorAction SilentlyContinue
if ($svc) {
    Write-Host "[1] Smartcore service found, status=$($svc.Status). Stopping + deleting..."
    if ($svc.Status -ne 'Stopped') {
        Stop-Service -Name 'Smartcore' -Force -ErrorAction SilentlyContinue
        for ($i = 0; $i -lt 20; $i++) {
            $s = Get-Service -Name 'Smartcore' -ErrorAction SilentlyContinue
            if (-not $s -or $s.Status -eq 'Stopped') { break }
            Start-Sleep -Milliseconds 250
        }
    }
    & sc.exe delete Smartcore | Out-Host
    Start-Sleep 1
    if (Get-Service -Name 'Smartcore' -ErrorAction SilentlyContinue) {
        Write-Host "    !! service still present after sc delete - reboot required"
    } else {
        Write-Host "    OK service removed"
    }
} else {
    Write-Host "[1] Smartcore service: not present"
}
Write-Host ""

# 2. ProgramData
$pd = "$env:ProgramData\Smartcore"
if (Test-Path $pd) {
    try { $sz = (Get-ChildItem $pd -Recurse -Force -ErrorAction SilentlyContinue | Measure-Object Length -Sum).Sum } catch { $sz = 0 }
    Write-Host ("[2] Removing {0}  ({1:N0} bytes)" -f $pd, $sz)
    Remove-Item -Path $pd -Recurse -Force -ErrorAction SilentlyContinue
    if (Test-Path $pd) { Write-Host "    FAIL still exists" } else { Write-Host "    removed OK" }
} else {
    Write-Host "[2] $pd : not present"
}
Write-Host ""

# 3. ProgramFiles
foreach ($pf in @("$env:ProgramFiles\Smartcore", "${env:ProgramFiles(x86)}\Smartcore")) {
    if ($pf -and (Test-Path $pf)) {
        try { $sz = (Get-ChildItem $pf -Recurse -Force -ErrorAction SilentlyContinue | Measure-Object Length -Sum).Sum } catch { $sz = 0 }
        Write-Host ("[3] Removing {0}  ({1:N0} bytes)" -f $pf, $sz)
        Remove-Item -Path $pf -Recurse -Force -ErrorAction SilentlyContinue
        if (Test-Path $pf) { Write-Host "    FAIL still exists" } else { Write-Host "    removed OK" }
    }
}
Write-Host ""
Write-Host "Done. Closing in 3 seconds..."
Start-Sleep 3
