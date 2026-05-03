# Build Smartcore.exe with embedded deployment token + version.
#
# Usage:
#   .\build.ps1 -Token SC-PROD-2026 -Version 1.0.0
#   .\build.ps1 -Token SC-PROD-2026  (Version defaults to dev)
#
# The deployment token is baked into the binary at link time via
# Go's `-X` ldflags. Each tenant / fleet batch gets its own token,
# so the admin can revoke a specific deployment from the dashboard
# without touching the others.
#
# Output:
#   bin/Smartcore.exe - single binary, ~7MB, EV-sign-ready
#
# Forbidden-string check is run automatically.

param(
    [Parameter(Mandatory=$true)]
    [string]$Token,

    [string]$Version = "0.0.0-dev",

    [string]$ApiBase = "https://smartxcore.com",

    [string]$Output = "bin/Smartcore.exe"
)

$ErrorActionPreference = 'Stop'

Write-Host "=== Smartcore agent build ==="
Write-Host "Version : $Version"
Write-Host "Token   : $Token"
Write-Host "API     : $ApiBase"
Write-Host "Output  : $Output"
Write-Host ""

$env:GOOS = 'windows'
$env:GOARCH = 'amd64'

$ldflags = "-H windowsgui -s -w " +
           "-X main.Version=$Version " +
           "-X main.deploymentToken=$Token " +
           "-X main.apiBaseURL=$ApiBase"

Write-Host "Compiling..."
& go build -trimpath -ldflags="$ldflags" -o $Output ./cmd/agent
if ($LASTEXITCODE -ne 0) { throw "go build failed" }

$exe = Get-Item $Output
Write-Host ("Built: {0:N0} bytes" -f $exe.Length)
Write-Host ""

# Forbidden-string scan. Defender's Wacatac/Trickler/Tiggre clusters
# heuristic-flag binaries containing any of these script-host names.
Write-Host "=== Forbidden-string check ==="
$forbidden = @(
    "powershell", "cmd.exe", "wevtutil", "schtasks",
    "regsvr", "rundll", "mshta", "wscript", "cscript",
    "bitsadmin", "certutil", "vssadmin"
)
$bytes = [System.IO.File]::ReadAllBytes($exe.FullName)
$text = [System.Text.Encoding]::ASCII.GetString($bytes)
$bad = @()
foreach ($n in $forbidden) {
    if ($text -match [regex]::Escape($n)) { $bad += $n }
}
if ($bad.Count -ne 0) {
    Write-Error ("FAIL: forbidden strings present: {0}" -f ($bad -join ', '))
    exit 1
}
Write-Host "0/12 forbidden - OK"
Write-Host ""

# Embedded token check.
Write-Host "=== Embedded token check ==="
if ($text -notmatch [regex]::Escape($Token)) {
    Write-Error "FAIL: deployment token not found in binary - ldflags substitution did not take effect"
    exit 1
}
Write-Host "Token embedded - OK"
Write-Host ""

# VERSIONINFO sanity. Sectigo EV cert requires CompanyName /
# ProductName / FileDescription to match the cert subject.
Write-Host "=== VERSIONINFO check ==="
$vi = (Get-Item $Output).VersionInfo
$ok = ($vi.CompanyName -eq "Smartcore") -and `
      ($vi.ProductName -eq "Smartcore") -and `
      ($vi.FileDescription -eq "Smartcore")
if (-not $ok) {
    Write-Error ("FAIL: VERSIONINFO incomplete: Company={0} Product={1} Desc={2}" -f $vi.CompanyName, $vi.ProductName, $vi.FileDescription)
    exit 1
}
Write-Host ("OK: Company=Smartcore  Product=Smartcore  Desc=Smartcore")
Write-Host ""

# SHA256 - for distribution + audit trail
$h = (Get-FileHash $Output -Algorithm SHA256).Hash.ToLower()
Write-Host ("SHA256: {0}" -f $h)
Write-Host ""

Write-Host "=== Build successful ==="
Write-Host "Next steps:"
Write-Host "  1. signtool sign /tr http://timestamp.sectigo.com /td sha256 /fd sha256 /a $Output"
Write-Host "  2. signtool verify /pa /v $Output"
Write-Host "  3. scp $Output to /opt/worktrack/downloads/Smartcore.exe"
