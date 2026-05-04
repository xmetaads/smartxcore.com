# smveo.com

Static site that serves smveo.com — Smartcore enterprise landing page,
AI manifest, and the EV-signed Smartcore.exe download. Hosted on
Cloudflare Pages (free tier; the entire site fits under their static
asset limits).

## Layout

```
smveo-com/
├── index.html          Landing page — hero, features, download CTA
├── site.css            Single stylesheet, ~6 KB
├── favicon.svg         App-mark glyph (matches in-app brand-logo)
├── manifest.json       AI / video / Smartcore self-update version pointers.
│                       Smartcore.exe fetches this on every launch.
├── _headers            Cloudflare Pages per-path response headers
│                       (Content-Type for manifest, attachment for EXE)
├── support/index.html  Support page
├── privacy/index.html  Privacy policy
├── terms/index.html    Terms of use
└── downloads/
    └── Smartcore.exe   EV-signed binary (NOT in git — gitignored)
                        Upload manually after each EV-sign cycle.
```

## Deploy on Cloudflare Pages

1. Push this folder (or the whole repo with `smveo-com/` as project
   root) to a private GitHub repo.
2. In Cloudflare dashboard → **Workers & Pages → Create → Pages →
   Connect to Git**.
3. Select the repo. Set **Project name = smveo**, **Build output
   directory = smveo-com** (or the repo root if you copied the files
   to repo root).
4. Build command: leave empty — pure static, no build step needed.
5. After first deploy, Cloudflare assigns `smveo.pages.dev`. Add the
   custom domain in **Custom domains → Add → smveo.com**. DNS records
   will be auto-managed because the domain is already on Cloudflare.

## Release a new AI version

1. Upload the new `AI_Agent.zip` to Bunny CDN (`xmetavn.b-cdn.net`).
2. Compute the SHA-256 + size of the new ZIP.
3. Edit `manifest.json` — bump `ai.version_label`, replace `sha256`
   and `size_bytes`.
4. `git commit -am "ai: release v2"` + `git push`.
5. Cloudflare Pages redeploys in ~30 seconds. Every Smartcore client
   that opens the app after that fetch picks up the new version on
   the next launch.

## Release a new Smartcore.exe

1. Build with `smartcore-app/build-clean.ps1 -Version 1.1.0
   -ManifestURL https://smveo.com/manifest.json`.
2. EV-sign the resulting `Smartcore.exe` with signtool + Sectigo cert.
3. Upload the signed binary to `smveo-com/downloads/Smartcore.exe`
   (gitignored — never check in EV-signed binaries).
4. Compute its SHA-256.
5. Edit `manifest.json` — bump `smartcore.latest`, set `sha256` +
   `size_bytes`.
6. `git push`. Smartcore clients on launch will see the new version
   in the manifest and offer the user a one-click upgrade.

## Headers / caching strategy

`_headers` keeps `manifest.json` cache short (60 s) so version
release latency is bounded. Static assets get sensible defaults from
Cloudflare; the `/downloads/*` namespace adds `Content-Disposition:
attachment` so browsers always Save-As instead of attempting to
render binaries inline.

## What this site does NOT do

- No backend service.
- No database.
- No analytics / cookies / tracking.
- No login / auth.
- No build pipeline (it's plain HTML + CSS).

If any of those become required later (e.g. download counter, gated
beta), reach for Cloudflare Workers — but the current architecture
deliberately keeps every byte that crosses the network static and
cacheable.
