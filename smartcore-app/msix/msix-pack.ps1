# msix-pack.ps1 - bundle Smartcore.exe + assets into a sideloadable
# .msix package, ready for EV-signing.
#
# Pattern matches what Anthropic ships for Claude Desktop (verified
# against the existing Claude install on this dev machine, see
# C:\Program Files\WindowsApps\Claude_<ver>_x64__<pubhash>\). Per-
# user install, no UAC, no service, sandboxed under WindowsApps.
#
# Pre-requisites (Windows SDK):
#   makeappx.exe  - usually under Windows Kits\10\bin\<sdkver>\x64\
#   signtool.exe  - same folder
#
# Workflow:
#   1. Build Smartcore.exe via build-clean.ps1
#   2. Run this script -> produces dist/Smartcore.msix (unsigned)
#   3. Sign with signtool sign /fd sha256 /a /v Smartcore.msix
#      (uses the same Sectigo EV cert that signs Smartcore.exe)
#   4. Distribute via GitHub Releases / smveo.com info page
#
# User experience after they download Smartcore.msix:
#   - Double-click in Explorer
#   - Windows shows the official "Install Smart Video?" dialog
#     (no UAC prompt, no admin needed)
#   - Click Install -> 2-5 seconds -> Smart Video window opens
#
# This is the gold-standard distribution path: highest Defender
# trust, cleanest install/uninstall, automatic AppContainer
# isolation. Falls back gracefully on Win10 < 1709 by also
# distributing the raw Smartcore.exe.

param(
    [string]$Version = "1.0.0",
    [string]$ProjectRoot = (Resolve-Path "$PSScriptRoot\..").Path,
    [string]$Output = (Join-Path (Resolve-Path "$PSScriptRoot\..\..").Path "dist")
)

$ErrorActionPreference = 'Stop'

$exePath = Join-Path $ProjectRoot "build\bin\Smartcore.exe"
if (-not (Test-Path $exePath)) {
    throw "Smartcore.exe not found at $exePath. Run build-clean.ps1 first."
}

if (-not (Test-Path $Output)) {
    New-Item -ItemType Directory -Path $Output | Out-Null
}

# Locate makeappx.exe in any installed Windows SDK.
$sdkRoot = "C:\Program Files (x86)\Windows Kits\10\bin"
$makeappx = Get-ChildItem $sdkRoot -Filter "makeappx.exe" -Recurse -ErrorAction SilentlyContinue |
            Where-Object { $_.FullName -match "\\x64\\" } |
            Sort-Object LastWriteTime -Descending |
            Select-Object -First 1
if (-not $makeappx) {
    throw "makeappx.exe not found under $sdkRoot. Install Windows SDK."
}
Write-Host "Using makeappx: $($makeappx.FullName)"

# Stage the package layout. Using the VFS (Virtual File System) layout
# matches Claude Desktop's MSIX layout - contents land in
# C:\Program Files\WindowsApps\<pkg>\VFS\ProgramFilesX64\SmartVideo\
# at install time, which is the convention Windows expects for
# desktop apps that aren't authored as UWP from the start.
$stage = Join-Path $env:TEMP "smartvideo-msix-stage"
if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
New-Item -ItemType Directory -Path $stage | Out-Null
New-Item -ItemType Directory -Path "$stage\VFS\ProgramFilesX64\SmartVideo" -Force | Out-Null
New-Item -ItemType Directory -Path "$stage\Assets" | Out-Null

# Copy executable + manifest. Patch the version into the manifest as
# bytes so PowerShell 5.1's pipeline doesn't mangle the Vietnamese
# diacritics in the Publisher DN (Get-Content/Set-Content with
# -Encoding UTF8 in PS 5.1 silently corrupts non-ASCII bytes during
# the round-trip - makeappx then reports "publisher attribute value
# must be valid as per publisher naming rules" because what was
# "CÔNG TY" is now "C?NG TY" in the file on disk).
Copy-Item $exePath "$stage\VFS\ProgramFilesX64\SmartVideo\Smartcore.exe"
$manifestSrc  = "$PSScriptRoot\AppxManifest.xml"
$manifestDest = "$stage\AppxManifest.xml"
$utf8noBom = New-Object System.Text.UTF8Encoding($false)
$utf8bom   = New-Object System.Text.UTF8Encoding($true)
$manifestText = [System.IO.File]::ReadAllText($manifestSrc, $utf8noBom)
$manifestText = $manifestText -replace 'Version="1\.0\.0\.0"', ('Version="' + $Version + '.0"')
# Write WITH BOM. makeappx.exe in newer SDKs treats a BOM-less XML
# file as ANSI/cp1252 even though the XML declaration says UTF-8 -
# the Vietnamese diacritics in the EV-cert Publisher DN ("CÔNG TY
# TNHH DU LỊCH...") then come out mangled and the manifest fails
# Publisher-attribute validation. With a BOM, makeappx reads the
# file as UTF-8 and the publisher comes through clean.
[System.IO.File]::WriteAllText($manifestDest, $manifestText, $utf8bom)

# Generate placeholder asset images. Production build should replace
# these with proper-sized PNGs designed for the brand. The MSIX
# specification REQUIRES at least StoreLogo, Square150x150Logo, and
# Square44x44Logo - missing assets cause makeappx to fail.
#
# Placeholder palette: Smart Video dark navy + accent blue (matches
# the in-app camera-roll-with-play logo SVG).
function Save-PlaceholderPng {
    param([string]$Path, [int]$Size)
    Add-Type -AssemblyName System.Drawing
    $bmp = New-Object System.Drawing.Bitmap($Size, $Size)
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.Clear([System.Drawing.Color]::FromArgb(255, 11, 13, 18))
    $brush = New-Object System.Drawing.SolidBrush([System.Drawing.Color]::FromArgb(255, 79, 158, 246))
    $accent = [int]($Size * 0.6)
    $offset = [int](($Size - $accent) / 2)
    $g.FillRectangle($brush, $offset, $offset, $accent, $accent)
    $g.Dispose()
    $bmp.Save($Path, [System.Drawing.Imaging.ImageFormat]::Png)
    $bmp.Dispose()
}
Save-PlaceholderPng "$stage\Assets\StoreLogo.png" 50
Save-PlaceholderPng "$stage\Assets\Square150x150Logo.png" 150
Save-PlaceholderPng "$stage\Assets\Square44x44Logo.png" 44

# Pack the staged tree into a .msix file.
$msixOut = Join-Path $Output "Smartcore.msix"
if (Test-Path $msixOut) { Remove-Item $msixOut -Force }

& $makeappx.FullName pack /d $stage /p $msixOut /o
if ($LASTEXITCODE -ne 0) { throw "makeappx pack failed with exit $LASTEXITCODE" }

Write-Host ""
Write-Host "=== MSIX created ==="
Write-Host "Path:   $msixOut"
$f = Get-Item $msixOut
Write-Host ("Size:   {0:N0} bytes ({1:N1} MB)" -f $f.Length, ($f.Length/1MB))
$h = (Get-FileHash $msixOut -Algorithm SHA256).Hash.ToLower()
Write-Host ("SHA256: {0}" -f $h)

Write-Host ""
Write-Host "Next: sign with signtool"
Write-Host "  signtool sign /fd sha256 /tr http://timestamp.sectigo.com /td sha256 /a `"$msixOut`""
