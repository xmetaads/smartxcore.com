#!/usr/bin/env bash
#
# update-download-url.sh - swap the Smartcore download URL across
# index.html + manifest.json in one shot, then redeploy.
#
# Use this every time the GitHub Releases tag changes (or you swap
# from GitHub Releases to Bunny CDN, or back). Keeps the website's
# big "Download" buttons in sync with manifest.json's smartcore.url
# field, which is what running Smartcore.exe instances poll for the
# self-update offer.
#
# Usage:
#
#   ./infrastructure/scripts/update-download-url.sh \
#       https://github.com/xmetaads/smartcore-releases/releases/download/v1.0.0/Smartcore.exe \
#       1.0.0 \
#       <sha256-of-the-signed-binary>
#
# Args:
#   $1  full HTTPS URL of the signed Smartcore.exe (no spaces)
#   $2  version string (e.g. "1.0.0")
#   $3  SHA-256 of the signed binary (hex, lowercase)

set -euo pipefail

if [ "$#" -ne 3 ]; then
    echo "usage: $0 <download_url> <version> <sha256>" >&2
    exit 1
fi

URL="$1"
VERSION="$2"
SHA256="$3"

# Sanity check on the SHA — must be 64 hex characters, lowercase.
# Otherwise the agent's hash check will fail at the first launch
# every fleet machine attempts the self-update.
if [[ ! "$SHA256" =~ ^[0-9a-f]{64}$ ]]; then
    echo "error: SHA-256 must be 64 lowercase hex chars, got: $SHA256" >&2
    exit 1
fi

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
INDEX="$REPO_ROOT/smveo-com/index.html"
MANIFEST="$REPO_ROOT/smveo-com/manifest.json"

# 1. Replace every js-download href with the new URL. Two anchors
#    on the page (hero CTA + bottom CTA), and we want both to land
#    at the same release.
python3 - "$INDEX" "$URL" <<'PY'
import re, sys
path, url = sys.argv[1], sys.argv[2]
with open(path, encoding="utf-8") as f: src = f.read()
src = re.sub(
    r'(class="[^"]*\bjs-download\b[^"]*"\s+href=)"[^"]*"',
    lambda m: f'{m.group(1)}"{url}"',
    src,
)
with open(path, "w", encoding="utf-8") as f: f.write(src)
PY
echo "  index.html  download URL updated"

# 2. Edit manifest.json's smartcore block. We use python instead of
#    sed because the SHA could in theory contain regex metachars and
#    JSON quoting is fiddly to get right with a one-liner sed.
python3 - "$MANIFEST" "$URL" "$VERSION" "$SHA256" <<'PY'
import json, sys
path, url, version, sha = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
with open(path, encoding="utf-8") as f: m = json.load(f)
m.setdefault("smartcore", {})
m["smartcore"]["latest"] = version
m["smartcore"]["url"] = url
m["smartcore"]["sha256"] = sha
# size_bytes is informational; left as 0 for now. Wire-side checks
# rely on sha256 alone for tamper detection.
with open(path, "w", encoding="utf-8") as f: json.dump(m, f, indent=2, ensure_ascii=False); f.write("\n")
PY
echo "  manifest.json  smartcore block updated"

# 3. Run deploy script if it exists.
if [ -x "$REPO_ROOT/infrastructure/scripts/deploy-smveo.sh" ]; then
    echo "  deploying..."
    "$REPO_ROOT/infrastructure/scripts/deploy-smveo.sh"
fi

echo "Done. Smartcore download now points at: $URL"
