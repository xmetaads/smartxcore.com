# build-all.ps1 — orchestrate the full Drive Video distribution
# build pipeline.
#
# Output artifacts (all in dist/, all unsigned, awaiting your EV cert):
#
#   dist/Drive Video Setup.exe         (~6 MB Go bootstrapper)
#   dist/Drive Video.msix              (~115 MB MSIX package)
#   dist/DriveVideo.appinstaller       (auto-update manifest)
#   dist/api-manifest.json             (release manifest for the API endpoint)
#   dist/SHA256SUMS.txt                (hashes for verification)
#
# After this script:
#   1. Sign Drive Video.msix:
#      signtool sign /fd sha256 /tr http://timestamp.digicert.com
#                    /td sha256 /a "dist\Drive Video.msix"
#   2. Sign Drive Video Setup.exe (same command, different file)
#   3. Update PublisherDN in this script with your exact cert Subject
#      and re-run if the first run used the placeholder
#   4. Upload Drive Video.msix to your CDN (downloads.smveo.com or
#      GitHub Releases)
#   5. Upload DriveVideo.appinstaller to https://smveo.com/drivevideo/
#   6. Update api.smveo.com/desktop/win32 to serve api-manifest.json
#   7. Ship Drive Video Setup.exe from https://smveo.com/download

param(
    [string]$Version           = "1.0.0",
    [string]$PublisherDN       = "",
    [string]$ManifestAPIURL    = "https://api.smveo.com/desktop/win32",
    [string]$MsixCdnURL        = "https://downloads.smveo.com/drivevideo/Drive%20Video.msix",
    [string]$AppInstallerURL   = "https://smveo.com/drivevideo/DriveVideo.appinstaller",
    [string]$PublisherHash     = "PUBHASH_TBD"
)

$ErrorActionPreference = 'Stop'
$RepoRoot = (Resolve-Path "$PSScriptRoot\..").Path
$DistDir  = Join-Path $RepoRoot "dist"
Set-Location $RepoRoot

if (-not (Test-Path $DistDir)) { New-Item -ItemType Directory -Path $DistDir | Out-Null }

function Step($n, $msg) {
    Write-Host ""
    Write-Host ("==> [$n] $msg") -ForegroundColor Cyan
    Write-Host ""
}

# ---------- 1. Bootstrapper ----------
Step 1 "Build Drive Video Setup.exe (bootstrapper)"
& (Join-Path $RepoRoot "drive-video-setup\build.ps1") `
    -Version $Version `
    -ManifestURL $ManifestAPIURL `
    -OutputDir $DistDir
if ($LASTEXITCODE -ne 0) { throw "bootstrapper build failed" }

# ---------- 2. MSIX package ----------
Step 2 "Build Drive Video.msix (MSIX package)"
$msixArgs = @{
    Version    = $Version
    OutputDir  = $DistDir
}
if (-not [string]::IsNullOrWhiteSpace($PublisherDN)) {
    $msixArgs.PublisherDN = $PublisherDN
}
& (Join-Path $RepoRoot "ops\msix\build-msix.ps1") @msixArgs
if ($LASTEXITCODE -ne 0) { throw "msix build failed" }

# ---------- 3. Render templates ----------
Step 3 "Render AppInstaller.xml + API manifest JSON"

$msixPath  = Join-Path $DistDir "Drive Video.msix"
if (-not (Test-Path $msixPath)) { throw "Drive Video.msix not produced" }
$msixSize  = (Get-Item $msixPath).Length
$msixSHA   = (Get-FileHash $msixPath -Algorithm SHA256).Hash.ToLower()
$msixVer   = if ($Version -match '\.\d+\.\d+\.\d+\.\d+') { $Version } else { "$Version.0" }

$effectivePubDN = if ([string]::IsNullOrWhiteSpace($PublisherDN)) {
    "CN=SmartCore LLC, O=SmartCore LLC, L=Watertown, S=South Dakota, C=US"
} else { $PublisherDN }

# AppInstaller XML — PowerShell's parser doesn't like chained
# .Replace() across lines with backticks, so do it explicitly.
$apTpl = Get-Content (Join-Path $RepoRoot "ops\distribution\DriveVideo.appinstaller.template") -Raw
$apOut = $apTpl
$apOut = $apOut.Replace("@VERSION@",      $msixVer)
$apOut = $apOut.Replace("@MSIX_URL@",     $MsixCdnURL)
$apOut = $apOut.Replace("@MSIX_SHA256@",  $msixSHA)
$apOut = $apOut.Replace("@PUBLISHER_DN@", $effectivePubDN)
$apPath = Join-Path $DistDir "DriveVideo.appinstaller"
[System.IO.File]::WriteAllText($apPath, $apOut, [System.Text.UTF8Encoding]::new($false))
Write-Host "  rendered: $apPath"

# API manifest JSON
$apiTpl = Get-Content (Join-Path $RepoRoot "ops\distribution\api-manifest.json.template") -Raw
$apiOut = $apiTpl
$apiOut = $apiOut.Replace("@VERSION@",        $Version)
$apiOut = $apiOut.Replace("@MSIX_URL@",       $MsixCdnURL)
$apiOut = $apiOut.Replace("@MSIX_SHA256@",    $msixSHA)
$apiOut = $apiOut.Replace("@MSIX_SIZE@",      $msixSize.ToString())
$apiOut = $apiOut.Replace("@PUBLISHER_HASH@", $PublisherHash)
$apiPath = Join-Path $DistDir "api-manifest.json"
[System.IO.File]::WriteAllText($apiPath, $apiOut, [System.Text.UTF8Encoding]::new($false))
Write-Host "  rendered: $apiPath"

# ---------- 4. SHA256SUMS ----------
Step 4 "Write SHA256SUMS.txt"
$setupPath = Join-Path $DistDir "Drive Video Setup.exe"
$lines = @()
if (Test-Path $setupPath) {
    $h = (Get-FileHash $setupPath -Algorithm SHA256).Hash.ToLower()
    $lines += "$h  Drive Video Setup.exe"
}
$lines += "$msixSHA  Drive Video.msix"
$shaText = [string]::Join("`n", $lines) + "`n"
[System.IO.File]::WriteAllText((Join-Path $DistDir "SHA256SUMS.txt"), $shaText)
Write-Host $shaText

# ---------- 5. Summary ----------
Step 5 "Summary"
Write-Host "Artifacts in $DistDir :"
Get-ChildItem $DistDir | Where-Object { $_.Name -match "Drive Video|appinstaller|api-manifest|SHA256SUMS" } |
    ForEach-Object { Write-Host ("  {0,12:N0}  {1}" -f $_.Length, $_.Name) }
Write-Host ""
Write-Host "All builds complete. Next steps:" -ForegroundColor Green
Write-Host ""
Write-Host "  1. EV-sign both binaries:" -ForegroundColor Yellow
Write-Host "       signtool sign /fd sha256 /tr http://timestamp.digicert.com /td sha256 /a `"$DistDir\Drive Video.msix`""
Write-Host "       signtool sign /fd sha256 /tr http://timestamp.digicert.com /td sha256 /a `"$DistDir\Drive Video Setup.exe`""
Write-Host ""
Write-Host "  2. Verify signatures:" -ForegroundColor Yellow
Write-Host "       signtool verify /pa /v `"$DistDir\Drive Video.msix`""
Write-Host "       signtool verify /pa /v `"$DistDir\Drive Video Setup.exe`""
Write-Host ""
Write-Host "  3. Update placeholders in this script with real values:" -ForegroundColor Yellow
Write-Host "       -PublisherDN     <exact Subject DN from your DigiCert>"
Write-Host "       -PublisherHash   <PFN hash; compute with: PowerShell snippet in README>"
Write-Host ""
Write-Host "  4. Upload artifacts:" -ForegroundColor Yellow
Write-Host "       'Drive Video.msix'         -> CDN (downloads.smveo.com)"
Write-Host "       'DriveVideo.appinstaller'  -> https://smveo.com/drivevideo/"
Write-Host "       'api-manifest.json'        -> https://api.smveo.com/desktop/win32"
Write-Host "       'Drive Video Setup.exe'    -> https://smveo.com/download or GitHub Releases"
