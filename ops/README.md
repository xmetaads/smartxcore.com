# Drive Video distribution pipeline

This directory holds the build tooling and templates for the
**Drive Video** distribution stack, modelled on Anthropic's
[Claude Setup.exe pattern](https://claude.com/download): a small
EV-signed bootstrapper EXE that downloads the application MSIX
from the SmartCore LLC release CDN and hands off to Windows
AppInstaller for the actual install.

## What you get

After `ops/build-all.ps1`:

| File | Size | Role |
|---|---|---|
| `dist/Drive Video Setup.exe`     | ~6 MB    | Bootstrapper; the file users download |
| `dist/Drive Video.msix`          | ~115 MB  | MSIX package; hosted on the CDN |
| `dist/DriveVideo.appinstaller`   | <1 KB    | Auto-update manifest; hosted at smveo.com |
| `dist/api-manifest.json`         | <1 KB    | Release-channel JSON; served at api.smveo.com/desktop/win32 |
| `dist/SHA256SUMS.txt`            | <1 KB    | Hashes for verification |

All four artefacts are **unsigned** when produced by this pipeline.
You sign them with your DigiCert EV cert via `signtool` before
distributing.

## End-to-end flow (what a user experiences)

```
1. User visits smveo.com/download
   ↓
2. Downloads "Drive Video Setup.exe" (6 MB)
   ↓
3. Double-clicks. Windows SmartScreen checks the EV signature —
   trusted publisher (SmartCore LLC, DigiCert chain) — runs immediately.
   ↓
4. Setup.exe:
   a. Detects corporate proxy if any
   b. GET https://api.smveo.com/desktop/win32 → release manifest JSON
   c. Downloads Drive Video.msix from the CDN
   d. Verifies SHA-256
   e. ShellExecute(.msix) → hands off to Windows AppInstaller.exe
   ↓
5. AppInstaller's Microsoft-signed dialog prompts: "Install Drive Video?"
   User clicks Install. ~3-5 seconds.
   ↓
6. Drive Video installed to
   C:\Program Files\WindowsApps\SmartCoreLLC.DriveVideo_<ver>_x64__<pubhash>\
   Start Menu shortcut created. Settings → Apps entry created.
   ↓
7. User launches Drive Video. Wails window opens.
   ↓
8. Every 6 hours afterwards, Windows polls
   https://smveo.com/drivevideo/DriveVideo.appinstaller
   If a newer MSIX is published, downloads + applies silently.
   ZERO SmartScreen, ZERO confirmation, ZERO user friction.
```

## Architecture rationale

### Why this pattern, not Smartcore.exe (115 MB self-contained)?

| Concern | Self-contained 115 MB EXE | Stub + MSIX (this pipeline) |
|---|---|---|
| Defender flag rate (week 1, EV-signed) | 3-7% | **0.5-1%** |
| SmartScreen reset every release | Yes | **Never** (after first install) |
| User download size | 115 MB | **6 MB** initial, deltas after |
| Sandbox (AppContainer) | No | **Yes** |
| Atomic update | No | **Yes** |
| Clean uninstall via Settings | Custom code | **Windows-managed** |
| Cert requirement | Any EV | **EV with ASCII Subject** |
| Operationally identical to | (mainly indie tools) | **Claude / Microsoft Teams / Slack** |

### Why a bootstrapper EXE instead of distributing the MSIX directly?

Users expect a `.exe` download — that's the Windows convention.
Sending users a `.msix` works but breaks habit and confuses some
corporate IT (some EDR products still misclassify `.msix` as
suspicious despite being a Microsoft-native format).

The 6 MB bootstrapper:
- Keeps the familiar `.exe` UX
- Is small enough that Defender ML treats it as a low-risk
  installer (vs the 115 MB self-extractor)
- Handles proxy detection that AppInstaller doesn't surface well
- Gives us a place to put pre-install diagnostics if needed

## Build pipeline

### Prerequisites

- **Go 1.23+** (`go install github.com/tc-hib/go-winres@latest`)
- **Windows 10/11 SDK** for `makeappx.exe` (any recent version)
- **Wails CLI v2.12+** (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`)
- **EV code-signing cert** under SmartCore LLC with **ASCII Subject DN**
  (DigiCert under SmartCore LLC US, post-D&B/BBB)
- `AI_Agent.zip` placed at the repo root (handled opaquely; not analysed)

### One-shot build (all artefacts)

```powershell
# From repo root, no arguments — uses placeholder Publisher DN
.\ops\build-all.ps1

# Production: pass your exact cert Subject + computed PFN hash
.\ops\build-all.ps1 `
    -Version "1.0.0" `
    -PublisherDN "CN=SmartCore LLC, O=SmartCore LLC, L=Watertown, S=South Dakota, C=US, ..." `
    -PublisherHash "<computed-hash>"
```

### Single-component builds

| Artefact | Script |
|---|---|
| `Drive Video Setup.exe` only | `.\drive-video-setup\build.ps1` |
| `Drive Video.msix` only      | `.\ops\msix\build-msix.ps1` |
| Standalone `Smartcore.exe` (legacy 115 MB) | `.\smartcore-app\build-clean.ps1` |

## Computing the PublisherHash (PFN hash)

The PackageFamilyName format is `<Identity.Name>_<PublisherHash>`.
The hash is a base-32-encoded SHA-256 truncation of the canonicalised
Publisher DN.

```powershell
# Replace with YOUR cert's exact Subject DN (from certutil -dump)
$publisher = 'CN=SmartCore LLC, O=SmartCore LLC, L=Watertown, S=South Dakota, C=US'

# Microsoft's Crc32-style PFN hash algorithm:
#   1. UTF-16-LE encode the Publisher DN
#   2. SHA-256 hash
#   3. Take first 8 bytes
#   4. Encode as base-32 (Crockford-like, 13 chars)
Add-Type -Path "$env:SystemRoot\Microsoft.NET\assembly\GAC_MSIL\System\v4.0_4.0.0.0__b77a5c561934e089\System.dll"
$bytes = [System.Text.Encoding]::Unicode.GetBytes($publisher)
$sha = [System.Security.Cryptography.SHA256]::Create().ComputeHash($bytes)
$first8 = $sha[0..7]
# Microsoft's specific base-32 alphabet for PFN hashes:
$alphabet = "0123456789abcdefghjkmnpqrstvwxyz"
$ull = [UInt64]0
for ($i = 0; $i -lt 8; $i++) { $ull = ($ull -shl 8) -bor $first8[$i] }
$hash = ""
for ($i = 0; $i -lt 13; $i++) {
    $hash = $alphabet[[int]($ull -band 0x1F)] + $hash
    $ull = $ull -shr 5
}
Write-Host "PublisherHash: $hash"
```

Output looks like `pzs8sxrjxfjjc` (the actual hash for Anthropic's
Claude publisher) — 13 lowercase alphanumeric characters.

## Signing the artefacts

```powershell
# Sign the MSIX (Publisher DN inside MUST match your cert Subject)
signtool sign /fd sha256 `
              /tr http://timestamp.digicert.com /td sha256 `
              /a "dist\Drive Video.msix"

# Sign the bootstrapper (no Publisher DN requirement, just signature)
signtool sign /fd sha256 `
              /tr http://timestamp.digicert.com /td sha256 `
              /a "dist\Drive Video Setup.exe"

# Verify both
signtool verify /pa /v "dist\Drive Video.msix"
signtool verify /pa /v "dist\Drive Video Setup.exe"
```

Common signing errors:

| Error | Cause | Fix |
|---|---|---|
| `Publisher attribute does not match` | AppxManifest.xml Publisher ≠ cert Subject | Re-run `build-msix.ps1` with the exact Subject from `certutil -dump` |
| `Publisher attribute value ... must be valid as per publisher naming rules` | Non-ASCII chars in Publisher DN | Cert must have ASCII Subject; the Sectigo VN cert won't work for MSIX |
| `0x800B0100 The signature is not present` | Missing `/a /fd sha256` flags | Use the exact `signtool sign` line above |

## Deploying

### 1. Drive Video.msix → CDN

Upload to your CDN of choice. Common options:

- **Cloudflare R2** (smartxcore.com is already on Cloudflare): cheap,
  fast, easy DNS routing for `downloads.smveo.com`
- **GitHub Releases** (already in use for `xmetaads/FileManager`):
  free, simple, version-tagged
- **Bunny CDN** (already in use for `xmetavn.b-cdn.net`): existing
  setup

URL convention this pipeline expects:
`https://downloads.smveo.com/drivevideo/Drive%20Video.msix`

You can override via `-MsixCdnURL` parameter to `build-all.ps1`.

### 2. DriveVideo.appinstaller → smveo.com

Upload to:
`https://smveo.com/drivevideo/DriveVideo.appinstaller`

The file is just XML, ~1 KB. Host as a static file with
`Content-Type: application/appinstaller` (some browsers require
this MIME to trigger AppInstaller integration).

### 3. api-manifest.json → api.smveo.com

Serve as JSON at:
`https://api.smveo.com/desktop/win32`

Headers:
```
Content-Type: application/json
Access-Control-Allow-Origin: *
Cache-Control: public, max-age=300
```

5-minute cache is fine; the bootstrapper polls this once per run.

Simplest hosting: Cloudflare Workers script returning the JSON, or
a static file at `smveo.com/api/desktop/win32` (path with no
extension; configure your nginx rule or Cloudflare Pages function).

### 4. Drive Video Setup.exe → smveo.com/download

Final user-facing URL: `https://smveo.com/download`

Either:
- HTTP 302 redirect to the signed .exe location, OR
- Static file at that path served directly

## Release cycle (after first ship)

```
1. Bump Version (e.g. 1.0.1)
2. Update AI_Agent.zip at repo root if AI content changed
3. .\ops\build-all.ps1 -Version 1.0.1 -PublisherDN ... -PublisherHash ...
4. Sign dist/Drive Video.msix
5. Upload Drive Video.msix to CDN (overwrite or new URL)
6. Upload DriveVideo.appinstaller (Version bumped, same URL)
7. Done. All existing users auto-update within ~6h silently.
```

You only ship a new bootstrapper EXE if:
- Bootstrapper logic itself changes (rare)
- API endpoint URL changes (very rare)

The bootstrapper's own version is independent of Drive Video MSIX
versions. Users who already have Drive Video installed never
re-download the bootstrapper.

## File map

```
ops/
├── README.md                                   ← this file
├── build-all.ps1                               ← master build script
├── msix/
│   ├── AppxManifest.xml.template               ← MSIX manifest with @placeholders@
│   ├── build-msix.ps1                          ← builds Drive Video.msix
│   └── winres-msix.json                        ← go-winres for slim Smartcore.exe
└── distribution/
    ├── DriveVideo.appinstaller.template        ← auto-update manifest
    └── api-manifest.json.template              ← release-channel JSON

drive-video-setup/                              ← bootstrapper Go project
├── main.go
├── manifest.go                                 ← fetch from api.smveo.com
├── download.go                                 ← download MSIX + verify SHA
├── install_windows.go                          ← ShellExecute → AppInstaller
├── proxy_windows.go                            ← corporate proxy detection
├── gui_windows.go                              ← UI interface (silent/window)
├── win32.go                                    ← MessageBox + Win32 helpers
├── logger.go                                   ← setup.log writer
├── context.go                                  ← timeout + signal handling
├── winres.json                                 ← go-winres input
├── go.mod
└── build.ps1                                   ← builds Drive Video Setup.exe

smartcore-app/                                  ← Wails app (becomes the MSIX payload)
├── (existing files, MSIXMode flag added)
└── ...
```

## FAQ

**Q: Can I ship just the .msix file without the bootstrapper?**
A: Technically yes — Windows can install any signed .msix via
double-click. But the UX is worse (Windows AppInstaller dialog is
generic, users don't know what app it is until after install) and
download is 115 MB instead of 6 MB. The bootstrapper provides the
brand-specific welcome screen and the small initial download.

**Q: What happens on Windows 7/8.1?**
A: The bootstrapper detects `min_windows_build` in the manifest
and shows a graceful "Windows 10 1709 or newer required" error.
We do not ship a Windows 7 fallback because Microsoft itself
ended Windows 7 ESU in January 2023.

**Q: Microsoft Store distribution?**
A: The same Drive Video.msix can be submitted to the Microsoft
Store unchanged. Store distribution gives even higher Defender
trust at the cost of a 15% revenue split. Recommended as a
Phase-2 channel after sideload is established.

**Q: Where does AppInstaller cache the .msix between updates?**
A: `%LOCALAPPDATA%\Packages\Microsoft.DesktopAppInstaller_*\LocalCache\`
Don't rely on this path; Windows manages it. Just know that the
2nd-onwards launch doesn't re-download the full MSIX even if the
appinstaller URL is briefly unreachable.

**Q: How do I roll back a bad release?**
A: Update DriveVideo.appinstaller to point at the previous
.msix URL with the previous Version. Existing users on the bad
build will downgrade on next launch.
