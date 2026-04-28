#!/usr/bin/env bash
#
# setup-vps.sh — provisions a fresh Ubuntu 22.04/24.04 VPS for WorkTrack.
# Run as root (or via sudo). Idempotent — re-running is safe.
#
# Usage:
#   curl -sSfL https://smartxcore.com/downloads/setup-vps.sh | sudo bash
#   ## or, after cloning the repo:
#   sudo ./infrastructure/scripts/setup-vps.sh

set -euo pipefail

DOMAIN="${DOMAIN:-smartxcore.com}"
ADMIN_EMAIL_FOR_LE="${ADMIN_EMAIL_FOR_LE:-admin@smartxcore.com}"
WORKTRACK_USER="worktrack"
WORKTRACK_HOME="/opt/worktrack"
PG_DB="worktrack"
PG_USER="worktrack"

step() { printf "\n\033[1;36m==> %s\033[0m\n" "$*"; }

require_root() {
  if [[ $EUID -ne 0 ]]; then
    echo "Run with sudo or as root" >&2
    exit 1
  fi
}

install_packages() {
  step "Installing system packages"
  apt-get update -y
  apt-get install -y \
    nginx \
    postgresql-16 \
    certbot \
    python3-certbot-nginx \
    ufw \
    curl \
    git \
    ca-certificates \
    gnupg \
    rsync \
    fail2ban
}

install_node() {
  step "Installing Node.js 22 (NodeSource)"
  if ! command -v node >/dev/null 2>&1 || [[ "$(node -v | sed 's/v//')" != 22.* ]]; then
    curl -fsSL https://deb.nodesource.com/setup_22.x | bash -
    apt-get install -y nodejs
  fi
  node -v && npm -v
}

create_user() {
  step "Creating system user $WORKTRACK_USER"
  if ! id "$WORKTRACK_USER" >/dev/null 2>&1; then
    useradd --system --create-home --home "$WORKTRACK_HOME" --shell /usr/sbin/nologin "$WORKTRACK_USER"
  fi
  mkdir -p "$WORKTRACK_HOME/backend" "$WORKTRACK_HOME/dashboard" "$WORKTRACK_HOME/downloads"
  chown -R "$WORKTRACK_USER:$WORKTRACK_USER" "$WORKTRACK_HOME"
}

setup_postgres() {
  step "Configuring PostgreSQL"
  systemctl enable --now postgresql
  if ! sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname='$PG_USER'" | grep -q 1; then
    PG_PASS="$(openssl rand -base64 24)"
    sudo -u postgres psql <<SQL
CREATE USER $PG_USER WITH ENCRYPTED PASSWORD '$PG_PASS';
CREATE DATABASE $PG_DB OWNER $PG_USER;
SQL
    echo
    echo "Generated DB password (save this — shown once):"
    echo "  DATABASE_URL=postgres://$PG_USER:$PG_PASS@localhost:5432/$PG_DB?sslmode=disable"
    echo
  else
    echo "Postgres user $PG_USER already exists — skipping creation"
  fi
}

setup_firewall() {
  step "Configuring UFW firewall"
  ufw default deny incoming
  ufw default allow outgoing
  ufw allow 22/tcp
  ufw allow 80/tcp
  ufw allow 443/tcp
  yes | ufw enable || true
  ufw status
}

setup_nginx() {
  step "Installing nginx config"
  install -m 0644 -o root -g root \
    "$(dirname "$0")/../nginx/proxy-headers.conf" \
    /etc/nginx/snippets/proxy-headers.conf

  install -m 0644 -o root -g root \
    "$(dirname "$0")/../nginx/smartxcore.conf" \
    /etc/nginx/sites-available/smartxcore.conf

  ln -sf /etc/nginx/sites-available/smartxcore.conf /etc/nginx/sites-enabled/smartxcore.conf
  rm -f /etc/nginx/sites-enabled/default

  nginx -t
  systemctl reload nginx
}

setup_tls() {
  step "Obtaining TLS certificate via Let's Encrypt"
  if [[ ! -f "/etc/letsencrypt/live/$DOMAIN/fullchain.pem" ]]; then
    certbot --nginx \
      --non-interactive \
      --agree-tos \
      --email "$ADMIN_EMAIL_FOR_LE" \
      -d "$DOMAIN" \
      -d "www.$DOMAIN"
  else
    echo "TLS cert already present for $DOMAIN"
  fi
  systemctl enable --now certbot.timer
}

install_systemd_units() {
  step "Installing systemd unit files"
  install -m 0644 -o root -g root \
    "$(dirname "$0")/../systemd/worktrack-backend.service" \
    /etc/systemd/system/worktrack-backend.service

  install -m 0644 -o root -g root \
    "$(dirname "$0")/../systemd/worktrack-dashboard.service" \
    /etc/systemd/system/worktrack-dashboard.service

  systemctl daemon-reload
  systemctl enable worktrack-backend.service
  systemctl enable worktrack-dashboard.service
}

print_next_steps() {
  cat <<EOF

================================================================
WorkTrack VPS provisioned. Next steps:

1. Build artifacts on your dev machine and copy them up:

   cd backend && make build
   scp bin/worktrack-server root@$DOMAIN:$WORKTRACK_HOME/backend/
   scp bin/worktrack-cli    root@$DOMAIN:$WORKTRACK_HOME/backend/
   scp -r migrations        root@$DOMAIN:$WORKTRACK_HOME/backend/

   cd dashboard && npm run build
   rsync -av .next/standalone/  root@$DOMAIN:$WORKTRACK_HOME/dashboard/
   rsync -av .next/static       root@$DOMAIN:$WORKTRACK_HOME/dashboard/.next/static
   rsync -av public             root@$DOMAIN:$WORKTRACK_HOME/dashboard/

2. Set up env files on the VPS:

   sudo -u $WORKTRACK_USER nano $WORKTRACK_HOME/backend/.env
   # Copy from backend/.env.example, fill DATABASE_URL, JWT_SECRET, SMTP_*, etc.

3. Run migrations and create the first admin:

   sudo -u $WORKTRACK_USER $WORKTRACK_HOME/backend/worktrack-cli migrate \
       -migrations $WORKTRACK_HOME/backend/migrations
   sudo -u $WORKTRACK_USER $WORKTRACK_HOME/backend/worktrack-cli create-admin \
       -migrations $WORKTRACK_HOME/backend/migrations \
       -email admin@smartxcore.com -name "Admin"

4. Start services:

   sudo systemctl start worktrack-backend
   sudo systemctl start worktrack-dashboard

5. Verify:

   curl https://$DOMAIN/healthz
   curl https://$DOMAIN/readyz
   open https://$DOMAIN  # log in

================================================================
EOF
}

main() {
  require_root
  install_packages
  install_node
  create_user
  setup_postgres
  setup_firewall
  setup_nginx
  setup_tls
  install_systemd_units
  print_next_steps
}

main "$@"
