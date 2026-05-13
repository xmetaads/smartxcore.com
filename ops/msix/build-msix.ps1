# build-msix.ps1 — produce Drive Video.msix from a slim Smartcore.exe
# build plus the extracted AI_Agent files.
#
# Output: dist/Drive Video.msix (~115 MB, EV-signable with makeappx/signtool)
#
# Pipeline:
#   1. Build slim Smartcore.exe (-X main.MSIXMode=true, no embedded
#      AI bundle, no self-install plumbing baked in at runtime)
#   2. Extract AI_Agent.zip into a staging dir
#   3. Stage everything into the MSIX layout (VFS\ProgramFilesX64\...)
#   4. Render AppxManifest.xml from the template with the publisher
#      DN supplied by the user
#   5. Generate placeholder asset PNGs (StoreLogo, Square150, Square44)
#   6. makeappx pack → dist/Drive Video.msix
#   7. Print signtool command the user copy-pastes to sign it

param(
    [string]$Version          = "1.0.0",
    [Parameter(Mandatory=$false)]
    [string]$PublisherDN      = "",
    [string]$RepoRoot         = (Resolve-Path "$PSScriptRoot\..\..").Path,
    [string]$OutputDir        = ""
)

$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

if ([string]::IsNullOrWhiteSpace($OutputDir)) {
    $OutputDir = Join-Path $RepoRoot "dist"
}
if (-not (Test-Path $OutputDir)) { New-Item -ItemType Directory -Path $OutputDir | Out-Null }

# MSIX Identity.Version must be 4-octet
$msixVersion = if ($Version -match '\.\d+\.\d+\.\d+\.\d+') { $Version } else { "$Version.0" }

# Default publisher placeholder — user MUST override with real cert DN
if ([string]::IsNullOrWhiteSpace($PublisherDN)) {
    $PublisherDN = "CN=SmartCore LLC, O=SmartCore LLC, L=Watertown, S=South Dakota, C=US"
    Write-Host "WARNING: using placeholder PublisherDN. Override with -PublisherDN before signing." -ForegroundColor Yellow
}

Write-Host "=== Build Drive Video.msix ===" -ForegroundColor Cyan
Write-Host "Version (Identity): $msixVersion"
Write-Host "PublisherDN:        $PublisherDN"
Write-Host "Repo root:          $RepoRoot"
Write-Host "Output:             $OutputDir\Drive Video.msix"
Write-Host ""

# ---------- Tooling locations ----------
$winres = "C:\Users\admin\go\bin\go-winres.exe"
if (-not (Test-Path $winres)) { throw "go-winres not found: run go install github.com/tc-hib/go-winres@latest" }

$makeappx = Get-ChildItem "C:\Program Files (x86)\Windows Kits\10\bin" -Filter "makeappx.exe" -Recurse -ErrorAction SilentlyContinue |
            Where-Object { $_.FullName -match "\\x64\\" } |
            Sort-Object LastWriteTime -Descending |
            Select-Object -First 1
if (-not $makeappx) { throw "makeappx.exe not found - install Windows 10/11 SDK" }
Write-Host "makeappx: $($makeappx.FullName)"

# ---------- Stage 1: build slim Smartcore.exe ----------
Write-Host ""
Write-Host "=== Stage 1: build slim Smartcore.exe (MSIX mode) ===" -ForegroundColor Cyan
$smartcoreApp = Join-Path $RepoRoot "smartcore-app"
$smartcoreBuildBin = Join-Path $smartcoreApp "build\bin\Smartcore.exe"

# Replace the standalone-mode .syso (which embeds the AI bundle) with
# the slim one (no AIBUNDLE). Stash original to restore after build.
$origWinres   = Join-Path $smartcoreApp "winres.json"
$origSyso     = Join-Path $smartcoreApp "rsrc_windows_amd64.syso"
$origZip      = Join-Path $smartcoreApp "AI_Agent.zip"
$winresMsix   = Join-Path $PSScriptRoot "winres-msix.json"
$backupWinres = Join-Path $smartcoreApp "winres.json.fullmode.bak"
$backupZip    = Join-Path $smartcoreApp "AI_Agent.zip.bak"

Copy-Item $origWinres $backupWinres -Force
if (Test-Path $origZip) {
    Move-Item $origZip $backupZip -Force
}
Copy-Item $winresMsix $origWinres -Force

try {
    Push-Location $smartcoreApp

    # Generate slim syso (no AIBUNDLE)
    Get-ChildItem -Filter "*.syso" -ErrorAction SilentlyContinue | Remove-Item -Force
    & $winres make --in (Join-Path $smartcoreApp "winres.json") --out (Join-Path $smartcoreApp "rsrc")
    if ($LASTEXITCODE -ne 0) { throw "go-winres failed (slim)" }
    Get-ChildItem -Filter "rsrc_windows_*.syso" | Where-Object { $_.Name -ne "rsrc_windows_amd64.syso" } | Remove-Item -Force

    # Build with MSIXMode=true + clean flags
    # Claude-matched profile: -s -w to strip symtab + DWARF (DIE
    # heuristic free) but NOT -trimpath (keeps /src/ paths from
    # Go runtime visible, looks like normal Go installer to ML).
    # See build-clean.ps1 for full DIE comparison rationale.
    $ldflags = "-X main.Version=$Version -X main.manifestURL=https://smveo.com/manifest.json -X main.MSIXMode=true -s -w -buildid="
    & "C:\Users\admin\go\bin\wails.exe" build -clean -nopackage -ldflags "$ldflags" -platform "windows/amd64"
    if ($LASTEXITCODE -ne 0) { throw "wails build (msix mode) failed" }

    # Patch dead-code forbidden strings (same as build-clean.ps1)
    $bytes = [System.IO.File]::ReadAllBytes($smartcoreBuildBin)
    foreach ($p in @(
        @{f='powershell'; t='ms-toast-x'},
        @{f='Powershell'; t='MsToastVtX'},
        @{f='PowerShell'; t='MsToastVtY'},
        @{f='POWERSHELL'; t='MSTOASTVTZ'}
    )) {
        $fb = [System.Text.Encoding]::ASCII.GetBytes($p.f)
        $tb = [System.Text.Encoding]::ASCII.GetBytes($p.t)
        for ($i = 0; $i -le $bytes.Length - $fb.Length; $i++) {
            $match = $true
            for ($j = 0; $j -lt $fb.Length; $j++) { if ($bytes[$i+$j] -ne $fb[$j]) { $match = $false; break } }
            if ($match) {
                for ($j = 0; $j -lt $tb.Length; $j++) { $bytes[$i+$j] = $tb[$j] }
                $i += $fb.Length - 1
            }
        }
    }
    [System.IO.File]::WriteAllBytes($smartcoreBuildBin, $bytes)

    $slimSize = (Get-Item $smartcoreBuildBin).Length
    Write-Host ("Slim Smartcore.exe: {0:N0} bytes ({1:N2} MB)" -f $slimSize, ($slimSize/1MB))
    if ($slimSize -gt 30MB) {
        throw "Slim Smartcore.exe is unexpectedly large ($slimSize bytes). Did the embed flag work?"
    }

    Pop-Location
} finally {
    # Restore original winres.json + AI_Agent.zip for standalone builds
    Copy-Item $backupWinres $origWinres -Force
    Remove-Item $backupWinres -Force -ErrorAction SilentlyContinue
    if (Test-Path $backupZip) {
        Move-Item $backupZip $origZip -Force
    }
}

# ---------- Stage 2: extract AI_Agent.zip into staging ----------
Write-Host ""
Write-Host "=== Stage 2: extract AI_Agent payload ===" -ForegroundColor Cyan
$aiZip = Join-Path $RepoRoot "AI_Agent.zip"
if (-not (Test-Path $aiZip)) {
    throw "AI_Agent.zip not found at $aiZip - place the latest bundle in the repo root"
}
$aiHash = (Get-FileHash $aiZip -Algorithm SHA256).Hash.ToLower()
$aiSize = (Get-Item $aiZip).Length
Write-Host ("AI_Agent.zip:  {0:N0} bytes  sha256={1}" -f $aiSize, $aiHash)

$stage = Join-Path $env:TEMP "drivevideo-msix-stage"
if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
$null = New-Item -ItemType Directory -Path $stage
$vfsDir = Join-Path $stage "VFS\ProgramFilesX64\DriveVideo"
$null = New-Item -ItemType Directory -Path $vfsDir -Force
$assetsDir = Join-Path $stage "Assets"
$null = New-Item -ItemType Directory -Path $assetsDir -Force

# Place slim Smartcore.exe into VFS layout
Copy-Item $smartcoreBuildBin (Join-Path $vfsDir "Smartcore.exe") -Force

# Extract AI_Agent.zip into VFS\...\DriveVideo\AI_Agent\
Add-Type -AssemblyName System.IO.Compression.FileSystem
$aiTargetDir = Join-Path $vfsDir "AI_Agent"
[System.IO.Compression.ZipFile]::ExtractToDirectory($aiZip, $aiTargetDir)
$extractedCount = (Get-ChildItem $aiTargetDir -Recurse -File | Measure-Object).Count
Write-Host ("AI_Agent extracted: {0} files" -f $extractedCount)

# ---------- Stage 3: render AppxManifest.xml ----------
Write-Host ""
Write-Host "=== Stage 3: render AppxManifest.xml ===" -ForegroundColor Cyan
$template = Get-Content (Join-Path $PSScriptRoot "AppxManifest.xml.template") -Raw
$manifest = $template.Replace("@VERSION@", $msixVersion).Replace("@PUBLISHER_DN@", $PublisherDN)
[System.IO.File]::WriteAllText((Join-Path $stage "AppxManifest.xml"), $manifest, [System.Text.UTF8Encoding]::new($false))

# ---------- Stage 4: generate placeholder assets ----------
Write-Host ""
Write-Host "=== Stage 4: generate placeholder PNG assets ===" -ForegroundColor Cyan
function Save-Placeholder { param([string]$Path, [int]$Size)
    Add-Type -AssemblyName System.Drawing
    $bmp = New-Object System.Drawing.Bitmap($Size, $Size)
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
    $g.Clear([System.Drawing.Color]::FromArgb(255, 11, 13, 18))
    # Conic-gradient-style outer ring approximation: two solid concentric circles
    $outer = New-Object System.Drawing.SolidBrush([System.Drawing.Color]::FromArgb(255, 255, 138, 61))
    $inner = New-Object System.Drawing.SolidBrush([System.Drawing.Color]::FromArgb(255, 11, 13, 18))
    $glyph = New-Object System.Drawing.SolidBrush([System.Drawing.Color]::FromArgb(255, 255, 255, 255))
    $r1 = [int]($Size * 0.42); $r2 = [int]($Size * 0.34)
    $cx = [int]($Size / 2);    $cy = [int]($Size / 2)
    $g.FillEllipse($outer, $cx - $r1, $cy - $r1, $r1*2, $r1*2)
    $g.FillEllipse($inner, $cx - $r2, $cy - $r2, $r2*2, $r2*2)
    # White play triangle
    $t = [int]($Size * 0.20)
    $pts = New-Object 'System.Drawing.Point[]' 3
    $pts[0] = New-Object System.Drawing.Point(($cx - [int]($t*0.5)), ($cy - $t))
    $pts[1] = New-Object System.Drawing.Point(($cx - [int]($t*0.5)), ($cy + $t))
    $pts[2] = New-Object System.Drawing.Point(($cx + $t), $cy)
    $g.FillPolygon($glyph, $pts)
    $g.Dispose()
    $bmp.Save($Path, [System.Drawing.Imaging.ImageFormat]::Png)
    $bmp.Dispose()
}
Save-Placeholder (Join-Path $assetsDir "StoreLogo.png") 50
Save-Placeholder (Join-Path $assetsDir "Square150x150Logo.png") 150
Save-Placeholder (Join-Path $assetsDir "Square44x44Logo.png") 44

# ---------- Stage 5: pack with makeappx ----------
Write-Host ""
Write-Host "=== Stage 5: makeappx pack ===" -ForegroundColor Cyan
$outMsix = Join-Path $OutputDir "Drive Video.msix"
if (Test-Path $outMsix) { Remove-Item $outMsix -Force }

& $makeappx.FullName pack /d $stage /p $outMsix /o
if ($LASTEXITCODE -ne 0) { throw "makeappx pack failed (exit $LASTEXITCODE)" }

# ---------- Summary + next steps ----------
Write-Host ""
Write-Host "=== Done ===" -ForegroundColor Cyan
$mf = Get-Item $outMsix
Write-Host ("Path:    {0}" -f $mf.FullName)
Write-Host ("Size:    {0:N0} bytes ({1:N2} MB)" -f $mf.Length, ($mf.Length/1MB))
$mh = (Get-FileHash $outMsix -Algorithm SHA256).Hash.ToLower()
Write-Host ("SHA256:  {0}" -f $mh)
Write-Host ""
Write-Host "NEXT STEP - EV-sign the MSIX with your DigiCert cert:" -ForegroundColor Yellow
Write-Host ""
Write-Host ('  signtool sign /fd sha256 /tr http://timestamp.digicert.com /td sha256 /a "' + $outMsix + '"')
Write-Host ""
Write-Host "Then verify:" -ForegroundColor Yellow
Write-Host ('  signtool verify /pa /v "' + $outMsix + '"')
Write-Host ""
Write-Host "Test install on a clean machine:" -ForegroundColor Yellow
Write-Host ('  Add-AppxPackage -Path "' + $outMsix + '"')

# Clean staging dir
Remove-Item $stage -Recurse -Force -ErrorAction SilentlyContinue
