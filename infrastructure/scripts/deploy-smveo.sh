#!/usr/bin/env bash
#
# deploy-smveo.sh - sync smveo-com/ + dist/Smartcore.exe to the VPS,
# verify, reload nginx if config changed.
#
# Run from the repo root on your workstation:
#
#   ./infrastructure/scripts/deploy-smveo.sh
#
# Prerequisites:
#   - SSH key at ~/.ssh/worktrack_deploy with access to root@smartxcore.com
#   - dist/Smartcore.exe built (smartcore-app/build-clean.ps1)
#   - smveo-com/ checked out at the repo root
#
# Idempotent: re-running on an unchanged tree is a near-no-op (rsync
# only transfers what's different).

set -euo pipefail

KEY="$HOME/.ssh/worktrack_deploy"
HOST="root@smartxcore.com"

step() { printf "\n\033[1;36m==> %s\033[0m\n" "$*"; }

step "Sync static site (smveo-com/) → /opt/smveo/"
# --delete removes files on the server that no longer exist locally,
# so renaming a page locally cleans up the old URL automatically.
# Excludes the gitignored downloads/ folder; binary deploy is below.
rsync -avz --delete --exclude='downloads/' \
  -e "ssh -i $KEY" \
  smveo-com/ "$HOST:/opt/smveo/"

step "Push Smartcore.exe"
if [ -f "dist/Smartcore.exe" ]; then
    rsync -avz \
      -e "ssh -i $KEY" \
      dist/Smartcore.exe "$HOST:/opt/smveo/downloads/Smartcore.exe"
else
    printf "   warn: dist/Smartcore.exe not found, skipping binary push\n"
fi

step "Push Smartcore.msix (if built)"
if [ -f "dist/Smartcore.msix" ]; then
    rsync -avz \
      -e "ssh -i $KEY" \
      dist/Smartcore.msix "$HOST:/opt/smveo/downloads/Smartcore.msix"
fi

step "Fix ownership + permissions"
ssh -i "$KEY" "$HOST" "chown -R www-data:www-data /opt/smveo && chmod -R 0755 /opt/smveo"

step "Sync nginx config"
scp -i "$KEY" infrastructure/nginx/smveo.conf "$HOST:/tmp/smveo.conf"
ssh -i "$KEY" "$HOST" "
    if ! cmp -s /tmp/smveo.conf /etc/nginx/sites-available/smveo.conf; then
        cp /tmp/smveo.conf /etc/nginx/sites-available/smveo.conf
        nginx -t && systemctl reload nginx
        echo '   nginx reloaded'
    else
        echo '   nginx config unchanged'
    fi
"

step "Health check"
ssh -i "$KEY" "$HOST" "
    echo 'manifest.json:'
    curl -sSI -k https://smveo.com/manifest.json | head -3 || true
    echo
    echo 'Smartcore.exe:'
    curl -sSI -k https://smveo.com/downloads/Smartcore.exe | head -3 || true
"

step "Done"
echo "Smartcore distribution live at https://smveo.com"
