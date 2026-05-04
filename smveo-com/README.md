# smveo.com

Static site for **Smart Video** — the world's first AI video platform.
Serves the public landing page, AI manifest, and the link to the
EV-signed Smartcore.exe download. Hosted on the smartxcore.com VPS,
proxied through Cloudflare.

## Layout

```
smveo-com/
├── index.html          Landing page — hero, features, download CTA
├── site.css            Single stylesheet
├── favicon.svg         Smart Video play-glyph mark
├── manifest.json       AI / video / Smartcore self-update version pointers.
│                       Smartcore.exe fetches this on every launch.
├── _headers            Cloudflare Pages-style per-path response headers
│                       (kept for documentation; current deploy uses
│                       nginx server block — see infrastructure/nginx/
│                       smveo.conf in the private repo)
├── support/index.html  Support page
├── privacy/index.html  Privacy policy
└── terms/index.html    Terms of use
```

The Smartcore.exe binary itself is NOT served from this site or repo.
It lives on GitHub Releases under `xmetaads/FileManager` (separate
public repo) and is referenced from `manifest.json` and the index
page anchors.

## Architecture

```
Browser ──HTTPS──▶ Cloudflare edge (orange cloud)
                       │ HTTPS, Full mode
                       ▼
                   smartxcore.com VPS — 180.93.238.83
                   nginx /opt/smveo/

Smartcore.exe download ──▶ github.com/xmetaads/FileManager/releases
                            (the public release-only repo)
```

This site exists only to serve the landing page and the manifest
pointer. The binary distribution is GitHub Releases, the AI bundle
is on Bunny CDN. Every byte is static and cacheable.

## Deploy

The active workflow runs from the private smartxcore.com repo:

```bash
./infrastructure/scripts/deploy-smveo.sh
```

That script rsyncs `smveo-com/` to `/opt/smveo/` on the VPS and
reloads nginx if the config has changed.

## Release workflow

To release a new Smartcore version:

1. Build via `smartcore-app/build-clean.ps1` (in the private repo).
2. EV-sign with signtool + the Sectigo cert.
3. Upload the signed binary to the GitHub Release tag on
   `xmetaads/FileManager` (e.g. tag `SAM` or `v1.0.0`).
4. Run `infrastructure/scripts/update-download-url.sh <url> <version> <sha>`
   to rewrite the download anchors and manifest, then auto-deploy.

To release a new AI version:

1. Upload the new `AI_Agent.zip` to Bunny CDN.
2. Edit `manifest.json` — bump `ai.version_label`, replace `sha256`
   and `size_bytes`.
3. Run `infrastructure/scripts/deploy-smveo.sh`.

Cloudflare cache propagates within ~60 seconds (manifest cache TTL)
and every Smart Video client picks up the new version on the next
launch.

## What this site does NOT do

- No backend service.
- No database.
- No analytics, cookies, or tracking.
- No login or auth.
- No build pipeline (it's plain HTML + CSS).

If any of those become required later, reach for Cloudflare Workers.
The current architecture deliberately keeps every byte that crosses
the network static and cacheable.
