# Deployment Guide

Bước-from-zero để deploy hệ thống lên production.

---

## Prerequisites

- Domain (ví dụ `congtycuaban.com`) đã trỏ NS về Cloudflare
- Cloudflare account
- VPS Ubuntu 22.04 (gợi ý: Hetzner CCX22 ~$30/tháng)
- PostgreSQL 16 (managed hoặc self-host trên VPS)
- Vercel account (cho dashboard)
- EV Code Signing Certificate

## 1. Database Setup

### Option A: Self-hosted Postgres trên VPS

```bash
# Trên VPS production
sudo apt update && sudo apt install postgresql-16
sudo -u postgres psql

CREATE USER worktrack WITH ENCRYPTED PASSWORD 'strong_random_password';
CREATE DATABASE worktrack OWNER worktrack;
\c worktrack
\i /path/to/migrations/001_initial.up.sql
```

### Option B: Managed Postgres (Supabase, Neon, RDS)

- Tạo Postgres 16 instance
- Lấy DATABASE_URL
- Run migrations:
```bash
migrate -path backend/migrations -database "$DATABASE_URL" up
```

## 2. Backend Deployment

### Option A: VPS với systemd

```bash
# Build binary trên local
cd backend
make build

# Copy lên VPS
scp bin/worktrack-server user@vps:/opt/worktrack/
scp .env user@vps:/opt/worktrack/

# Trên VPS — tạo systemd service
sudo tee /etc/systemd/system/worktrack-backend.service <<EOF
[Unit]
Description=WorkTrack Backend
After=network.target postgresql.service

[Service]
Type=simple
User=worktrack
WorkingDirectory=/opt/worktrack
EnvironmentFile=/opt/worktrack/.env
ExecStart=/opt/worktrack/worktrack-server
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl enable --now worktrack-backend
```

### Option B: Docker Compose

```bash
cd infrastructure
docker compose -f docker-compose.yml up -d
```

### Option C: Cloud Run / Lambda

Backend là 1 Go binary, deploy được mọi nơi. Refer to provider docs.

## 3. nginx Reverse Proxy + TLS

```nginx
server {
    listen 443 ssl http2;
    server_name api.congtycuaban.com;

    ssl_certificate /etc/letsencrypt/live/api.congtycuaban.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/api.congtycuaban.com/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;

    # Security headers
    add_header Strict-Transport-Security "max-age=31536000" always;
    add_header X-Content-Type-Options "nosniff" always;
    add_header X-Frame-Options "DENY" always;

    # Rate limiting (extra layer above app-level)
    limit_req zone=agent_api burst=200 nodelay;

    location / {
        proxy_pass http://localhost:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

Get TLS cert:
```bash
sudo certbot --nginx -d api.congtycuaban.com
```

## 4. Cloudflare Setup

1. Add `api.congtycuaban.com` as proxied A record → VPS IP
2. SSL/TLS mode: **Full (strict)**
3. Edge Certificates: HSTS enabled
4. WAF: enable managed rules
5. Rate Limiting: 60 req/min per IP for `/api/v1/agent/*`

## 5. Dashboard Deployment

### Option A: Vercel

```bash
cd dashboard
npx vercel --prod
```

Set env vars in Vercel dashboard:
- `NEXT_PUBLIC_API_BASE_URL=https://api.congtycuaban.com`

### Option B: VPS với nginx

```bash
cd dashboard
npm run build

# Copy out/ to /var/www/track/
sudo rsync -av out/ user@vps:/var/www/track/
```

## 6. Object Storage (Cloudflare R2)

```bash
# Tạo bucket trong Cloudflare dashboard
# Tạo R2 API token với scope Read+Write

# Upload installer
wrangler r2 object put worktrack/setup.exe --file=installer/dist/setup.exe
```

Custom domain: `cdn.congtycuaban.com` → R2 bucket public.

## 7. Initial Admin Bootstrap

```bash
# Trên VPS
psql $DATABASE_URL <<SQL
INSERT INTO admin_users (email, password_hash, name, role)
VALUES (
  'admin@congtycuaban.com',
  '<bcrypt_hash_of_initial_password>',
  'Admin Name',
  'admin'
);
SQL

# Generate bcrypt hash on local
htpasswd -bnBC 12 "" 'your_strong_password' | tr -d ':\n'
```

## 8. Monitoring Setup

```bash
# Prometheus on VPS
sudo apt install prometheus
# Edit /etc/prometheus/prometheus.yml to scrape /metrics

# Grafana
sudo apt install grafana
sudo systemctl enable --now grafana-server
```

Import dashboard from `infrastructure/grafana/worktrack.json` (TODO).

## 9. Backup

Cron daily backup:
```bash
sudo crontab -e

0 3 * * * pg_dump $DATABASE_URL | gzip | aws s3 cp - s3://worktrack-backup/db-$(date +\%Y\%m\%d).sql.gz
```

## 10. Verify Deployment

```bash
# Health check
curl https://api.congtycuaban.com/healthz
# {"status":"ok","version":"0.1.0",...}

curl https://api.congtycuaban.com/readyz
# {"status":"ready"...}
```

Dashboard: https://track.congtycuaban.com

## 11. Rollback Plan

Nếu deployment có vấn đề:

```bash
# Backend rollback
sudo systemctl stop worktrack-backend
sudo cp /opt/worktrack/worktrack-server.previous /opt/worktrack/worktrack-server
sudo systemctl start worktrack-backend

# Database rollback
migrate -path backend/migrations -database "$DATABASE_URL" down 1

# Dashboard rollback (Vercel)
# Use Vercel dashboard → Deployments → Promote previous to Production
```

## 12. Checklist

- [ ] Domain DNS configured (api, track, cdn)
- [ ] TLS certificates valid (verify with SSLLabs A+)
- [ ] Database migrations applied
- [ ] Backend health endpoints respond
- [ ] Dashboard loads at https://track.x
- [ ] Admin user created
- [ ] First onboarding token created
- [ ] Test agent registration end-to-end
- [ ] Test PowerShell remote execution
- [ ] Backups running daily
- [ ] Alerts configured
- [ ] Cloudflare WAF + rate limiting active
- [ ] Documentation accessible to backup person
