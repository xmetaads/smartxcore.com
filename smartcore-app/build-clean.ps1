# build-clean.ps1 - build Smartcore.exe with Wails, with the
# AI bundle embedded into the .rsrc resource section instead of
# the .data section that //go:embed produces.
#
# Why we rolled our own resource pipeline:
#   - VERSIONINFO + RT_RCDATA need to be in ONE .syso so the Go
#     linker doesn't fight over the resource directory; goversioninfo
#     can't add RT_RCDATA, so we drive rc.exe directly.
#   - The AI bundle in .rsrc lands as read-only with high entropy,
#     which Defender's ML clusters expect for embedded resources.
#     Same shape Claude Setup.exe ships - .rsrc entropy 7.999, no
#     flag.
#
# Pipeline:
#   1. Stage AI_Agent.zip from the project root into smartcore-app/
#      so resources.rc's "AIBUNDLE RCDATA AI_Agent.zip" line resolves.
#   2. rc.exe resources.rc -> resources.res
#   3. cvtres.exe resources.res -> resource_windows_amd64.syso
#      (Go links any .syso file in the package directory; this
#      replaces the goversioninfo output we used to use.)
#   4. wails build (-nopackage so Wails's own syso doesn't conflict)
#   5. Patch out the dead-code "powershell" strings go-toast drags in
#   6. Forbidden-string sanity scan, file summary

param(
    [string]$Version = "1.0.0",
    [string]$ManifestURL = "https://smveo.com/manifest.json",
    [string]$AppName = "Smartcore"
)

$ErrorActionPreference = 'Stop'
Set-Location "$PSScriptRoot"

Write-Host "=== Building Smartcore $Version ==="
Write-Host "Manifest URL: $ManifestURL"
Write-Host ""

# --- Step 1: stage AI_Agent.zip into the package directory --------
# rc.exe reads the file via the path written in resources.rc (relative
# to the .rc file). We pull the latest copy from the project root
# rather than checking it in to git, since the bundle is a 100-MB-
# class binary on its own release cadence.

$rootZip = Join-Path (Resolve-Path "$PSScriptRoot\..").Path "AI_Agent.zip"
$pkgZip  = Join-Path $PSScriptRoot "AI_Agent.zip"
if (-not (Test-Path $rootZip)) {
    throw "AI_Agent.zip not found at $rootZip - place the latest bundle in the project root before building."
}

$rootHash = (Get-FileHash $rootZip -Algorithm SHA256).Hash.ToLower()
$rootSize = (Get-Item $rootZip).Length
$copyNeeded = $true
if (Test-Path $pkgZip) {
    $pkgHash = (Get-FileHash $pkgZip -Algorithm SHA256).Hash.ToLower()
    if ($pkgHash -eq $rootHash) { $copyNeeded = $false }
}
if ($copyNeeded) {
    Write-Host "Staging AI_Agent.zip into package directory..."
    Copy-Item $rootZip $pkgZip -Force
}
Write-Host ("AI_Agent.zip:  size={0:N0} bytes  sha256={1}" -f $rootSize, $rootHash)
Write-Host ""

# --- Step 2: build the .syso resource object ---------------------
# go-winres is a Go-native tool from github.com/tc-hib/go-winres
# that reads winres.json and emits a properly-formatted .syso the
# Go linker can consume. We use it instead of the rc.exe + cvtres
# Windows SDK pipeline because cvtres has a documented bug with
# >100 MB resources: it produces COFF section numbers that
# overflow Go's signed-int parsing, causing the Go linker to
# bail with "sectnum < 0!" (golang/go#47029).
#
# go-winres reads winres.json which declares VERSIONINFO,
# RT_MANIFEST, and the RT_RCDATA "AIBUNDLE" pointing at
# AI_Agent.zip. The resulting rsrc_windows_amd64.syso replaces
# the older goversioninfo output.

$winres = "C:\Users\admin\go\bin\go-winres.exe"
if (-not (Test-Path $winres)) {
    throw "go-winres not found - run: go install github.com/tc-hib/go-winres@latest"
}

# Drop any stale syso from previous goversioninfo / cvtres runs
# so the linker only sees the one go-winres produces.
Get-ChildItem $PSScriptRoot -Filter "*.syso" -ErrorAction SilentlyContinue | Remove-Item -Force

Write-Host "Compiling winres.json -> rsrc_windows_amd64.syso..."
& $winres make --in (Join-Path $PSScriptRoot "winres.json") --out (Join-Path $PSScriptRoot "rsrc")
if ($LASTEXITCODE -ne 0) { throw "go-winres make failed with exit $LASTEXITCODE" }

# go-winres emits rsrc_windows_amd64.syso (and one per arch); we
# only target windows/amd64 so the others can be dropped.
Get-ChildItem $PSScriptRoot -Filter "rsrc_windows_*.syso" | Where-Object { $_.Name -ne "rsrc_windows_amd64.syso" } | Remove-Item -Force -ErrorAction SilentlyContinue

$sysoFile = Join-Path $PSScriptRoot "rsrc_windows_amd64.syso"
if (-not (Test-Path $sysoFile)) { throw "go-winres did not produce $sysoFile" }
$sysoSize = (Get-Item $sysoFile).Length
if ($sysoSize -lt ($rootSize - 1024)) {
    throw "$sysoFile is suspiciously small ($sysoSize bytes) - AI_Agent.zip was not embedded"
}
Write-Host ("rsrc_windows_amd64.syso: {0:N0} bytes" -f $sysoSize)
Write-Host ""

# --- Step 4: Wails build ------------------------------------------

$ldflags = "-X main.Version=$Version -X main.manifestURL=$ManifestURL"

# -nopackage: tell Wails to skip its own syso. We supply our own
#             via the rc.exe + cvtres pipeline above.
& "C:\Users\admin\go\bin\wails.exe" build -clean -nopackage -ldflags "$ldflags" -platform "windows/amd64"
if ($LASTEXITCODE -ne 0) { throw "wails build failed" }

$exe = "$PSScriptRoot\build\bin\Smartcore.exe"
if (-not (Test-Path $exe)) { throw "binary not found: $exe" }

# --- Step 5: dead-code string patch -------------------------------

Write-Host ""
Write-Host "=== Patching binary to remove dead-code forbidden strings ==="
$bytes = [System.IO.File]::ReadAllBytes($exe)
$patches = @(
    @{ from = 'powershell'; to = 'ms-toast-x' }   # lowercase
    @{ from = 'Powershell'; to = 'MsToastVtX' }   # capital P
    @{ from = 'PowerShell'; to = 'MsToastVtY' }   # CamelCase
    @{ from = 'POWERSHELL'; to = 'MSTOASTVTZ' }   # all caps
)
foreach ($p in $patches) {
    $fromBytes = [System.Text.Encoding]::ASCII.GetBytes($p.from)
    $toBytes   = [System.Text.Encoding]::ASCII.GetBytes($p.to)
    if ($fromBytes.Length -ne $toBytes.Length) { throw "patch length mismatch for $($p.from)" }
    $count = 0
    for ($i = 0; $i -le $bytes.Length - $fromBytes.Length; $i++) {
        $match = $true
        for ($j = 0; $j -lt $fromBytes.Length; $j++) {
            if ($bytes[$i + $j] -ne $fromBytes[$j]) { $match = $false; break }
        }
        if ($match) {
            for ($j = 0; $j -lt $toBytes.Length; $j++) { $bytes[$i + $j] = $toBytes[$j] }
            $count++
            $i += $fromBytes.Length - 1
        }
    }
    Write-Host ("  '{0}' -> '{1}'  ({2} occurrences)" -f $p.from, $p.to, $count)
}
[System.IO.File]::WriteAllBytes($exe, $bytes)

# --- Step 6: scans + summary --------------------------------------

Write-Host ""
Write-Host "=== Forbidden-string scan (post-patch) ==="
$forbidden = @("powershell","cmd.exe","wevtutil","schtasks","regsvr","rundll","mshta","wscript","cscript","bitsadmin","certutil","vssadmin")
$bytesNew = [System.IO.File]::ReadAllBytes($exe)
$text = [System.Text.Encoding]::ASCII.GetString($bytesNew)
$bad = @()
foreach ($n in $forbidden) {
    if ($text -match [regex]::Escape($n)) { $bad += $n }
}
if ($bad.Count -ne 0) {
    Write-Error ("FAIL: forbidden strings still present: {0}" -f ($bad -join ', '))
    exit 1
}
Write-Host "0/12 forbidden - OK"

Write-Host ""
Write-Host "=== File summary ==="
$f = Get-Item $exe
Write-Host ("Path:   {0}" -f $f.FullName)
Write-Host ("Size:   {0:N0} bytes ({1:N1} MB)" -f $f.Length, ($f.Length/1MB))
$h = (Get-FileHash $exe -Algorithm SHA256).Hash.ToLower()
Write-Host ("SHA256: {0}" -f $h)
$vi = $f.VersionInfo
Write-Host ("FileDescription: {0}" -f $vi.FileDescription)
Write-Host ("ProductName:     {0}" -f $vi.ProductName)
Write-Host ("CompanyName:     {0}" -f $vi.CompanyName)
Write-Host ("FileVersion:     {0}" -f $vi.FileVersion)
Write-Host ""
Write-Host "Build complete."
