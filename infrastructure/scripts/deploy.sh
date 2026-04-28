#!/usr/bin/env bash
#
# deploy.sh — copy locally-built artifacts to the production VPS and
# restart services. Run from the repo root on your workstation.
#
# Required env vars:
#   DEPLOY_HOST   target host (e.g. smartxcore.com or root@1.2.3.4)
# Optional:
#   DEPLOY_PATH   defaults to /opt/worktrack
#
# Example:
#   DEPLOY_HOST=root@smartxcore.com ./infrastructure/scripts/deploy.sh

set -euo pipefail

: "${DEPLOY_HOST:?Set DEPLOY_HOST env var}"
DEPLOY_PATH="${DEPLOY_PATH:-/opt/worktrack}"

step() { printf "\n\033[1;36m==> %s\033[0m\n" "$*"; }

step "Building backend"
( cd backend && make build )

step "Building dashboard"
( cd dashboard && npm run build )

step "Building agent (Windows)"
( cd agent && make build-windows )

step "Copying backend"
ssh "$DEPLOY_HOST" "mkdir -p $DEPLOY_PATH/backend"
rsync -avz --delete \
  backend/bin/worktrack-server \
  backend/bin/worktrack-cli \
  "$DEPLOY_HOST:$DEPLOY_PATH/backend/"
rsync -avz --delete backend/migrations/ "$DEPLOY_HOST:$DEPLOY_PATH/backend/migrations/"

step "Copying dashboard (Next.js standalone)"
ssh "$DEPLOY_HOST" "mkdir -p $DEPLOY_PATH/dashboard"
rsync -avz --delete dashboard/.next/standalone/ "$DEPLOY_HOST:$DEPLOY_PATH/dashboard/"
rsync -avz --delete dashboard/.next/static/     "$DEPLOY_HOST:$DEPLOY_PATH/dashboard/.next/static/"
rsync -avz --delete dashboard/public/           "$DEPLOY_HOST:$DEPLOY_PATH/dashboard/public/"

step "Copying agent installer payload to /downloads"
ssh "$DEPLOY_HOST" "mkdir -p $DEPLOY_PATH/downloads"
rsync -avz agent/bin/worktrack-agent.exe "$DEPLOY_HOST:$DEPLOY_PATH/downloads/"

step "Restarting services"
ssh "$DEPLOY_HOST" "
  sudo chown -R worktrack:worktrack $DEPLOY_PATH
  sudo systemctl restart worktrack-backend
  sudo systemctl restart worktrack-dashboard
  sleep 3
  sudo systemctl status worktrack-backend --no-pager -n 5
  sudo systemctl status worktrack-dashboard --no-pager -n 5
"

step "Health check"
curl -fsS "https://${DEPLOY_HOST#*@}/healthz" || true
echo

step "Done"
