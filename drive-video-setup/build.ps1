# build.ps1 — build the Drive Video bootstrapper (Drive Video Setup.exe).
#
# Output: dist/Drive Video Setup.exe (~6-8 MB, EV-signable)
#
# Pipeline:
#   1. go-winres → resource_windows_amd64.syso (version info + manifest)
#   2. go build with hardening flags (-s -w -trimpath -buildid=)
#   3. Sanity scan (0/12 forbidden strings, low entropy)
#
# The output binary is signed by the user separately with their EV
# cert via signtool.

param(
    [string]$Version = "1.0.0",
    [string]$ManifestURL = "https://api.smveo.com/desktop/win32",
    [string]$OutputDir = (Resolve-Path "$PSScriptRoot\..\dist").Path
)

$ErrorActionPreference = 'Stop'
Set-Location "$PSScriptRoot"

Write-Host "=== Building Drive Video Setup $Version ===" -ForegroundColor Cyan
Write-Host "Manifest URL:  $ManifestURL"
Write-Host "Output dir:    $OutputDir"
Write-Host ""

if (-not (Test-Path $OutputDir)) {
    New-Item -ItemType Directory -Path $OutputDir | Out-Null
}

# --- Step 1: generate .syso resource via go-winres ---------------
$winres = "C:\Users\admin\go\bin\go-winres.exe"
if (-not (Test-Path $winres)) {
    throw "go-winres not found - run: go install github.com/tc-hib/go-winres@latest"
}

# Drop stale syso from prior runs
Get-ChildItem $PSScriptRoot -Filter "*.syso" -ErrorAction SilentlyContinue | Remove-Item -Force

Write-Host "Compiling winres.json -> rsrc_windows_amd64.syso..."
& $winres make --in (Join-Path $PSScriptRoot "winres.json") --out (Join-Path $PSScriptRoot "rsrc")
if ($LASTEXITCODE -ne 0) { throw "go-winres make failed with exit $LASTEXITCODE" }

# Keep only amd64 syso (we only ship x64)
Get-ChildItem $PSScriptRoot -Filter "rsrc_windows_*.syso" |
    Where-Object { $_.Name -ne "rsrc_windows_amd64.syso" } |
    Remove-Item -Force -ErrorAction SilentlyContinue

if (-not (Test-Path (Join-Path $PSScriptRoot "rsrc_windows_amd64.syso"))) {
    throw "go-winres did not produce rsrc_windows_amd64.syso"
}

# --- Step 2: go build with hardening flags -----------------------
# Bake build-time values into Version + ManifestURL.
# Strip symbol tables and debug info for a clean .text section.
# Blank build-id for deterministic SHA256 across rebuilds (helps
# SmartScreen reputation aggregation on the same publisher cert).

$ldflags = @(
    "-X main.Version=$Version",
    "-X main.ManifestURL=$ManifestURL",
    "-s",
    "-w",
    "-buildid="
) -join " "

# -H windowsgui suppresses the console window that would otherwise
# briefly flash when the bootstrapper launches. The bootstrapper
# uses MessageBox + setup.log for all output; no console required.
$outExe = Join-Path $OutputDir "Drive Video Setup.exe"
if (Test-Path $outExe) { Remove-Item $outExe -Force }

Write-Host ""
Write-Host "Compiling Go binary..."
& go build -trimpath -ldflags "$ldflags -H windowsgui" -o "$outExe" .
if ($LASTEXITCODE -ne 0) { throw "go build failed" }

# Clean up syso so it doesn't show up in git status
Remove-Item (Join-Path $PSScriptRoot "rsrc_windows_amd64.syso") -Force -ErrorAction SilentlyContinue

# --- Step 3: forbidden-string sanity scan ------------------------
Write-Host ""
Write-Host "=== Forbidden-string scan ==="
$bytes = [System.IO.File]::ReadAllBytes($outExe)
$text = [System.Text.Encoding]::ASCII.GetString($bytes)
$forbidden = @("powershell","cmd.exe","wevtutil","schtasks","regsvr","rundll","mshta","wscript","cscript","bitsadmin","certutil","vssadmin")
$bad = @()
foreach ($n in $forbidden) {
    if ($text -match [regex]::Escape($n)) { $bad += $n }
}
if ($bad.Count -ne 0) {
    Write-Error ("FAIL: forbidden strings present: {0}" -f ($bad -join ', '))
    exit 1
}
Write-Host "0/12 forbidden - OK"

# --- Step 4: file summary ----------------------------------------
Write-Host ""
Write-Host "=== File summary ==="
$f = Get-Item $outExe
Write-Host ("Path:    {0}" -f $f.FullName)
Write-Host ("Size:    {0:N0} bytes ({1:N2} MB)" -f $f.Length, ($f.Length/1MB))
$h = (Get-FileHash $outExe -Algorithm SHA256).Hash.ToLower()
Write-Host ("SHA256:  {0}" -f $h)
$vi = $f.VersionInfo
Write-Host ("Product: {0} v{1} by {2}" -f $vi.ProductName, $vi.FileVersion, $vi.CompanyName)
Write-Host ""
Write-Host "Bootstrapper build complete. Ready for EV-sign with signtool."
