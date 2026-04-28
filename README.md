# WorkTrack — Internal RMM System

> Hệ thống RMM (Remote Monitoring & Management) tự xây cho công ty tuyển dụng nhân sự, thay thế NinjaOne.
> Tracking online/offline + work time + remote PowerShell execution. Privacy-first, minimal footprint.

---

## Mục tiêu dự án

| | |
|---|---|
| Số endpoint | ~2,000 máy Windows toàn cầu |
| Use case | Track online/offline + work time + chạy PowerShell từ xa |
| Triết lý | Privacy-first, code own, data own, vendor-portable |
| Chi phí mục tiêu | < $200/tháng (so với $15,000/tháng NinjaOne) |
| Tiết kiệm dự kiến | ~$178,000/năm |

## Kiến trúc 3 thành phần

```
┌──────────────────────┐    HTTPS    ┌──────────────────────┐    HTTPS    ┌──────────────────────┐
│  Agent (Go)          │ ──────────▶ │  Backend (Go)        │ ◀────────── │  Dashboard (Next.js) │
│  Windows endpoint    │             │  REST API            │             │  Admin browser       │
│  ~10MB binary        │             │  PostgreSQL          │             │                      │
└──────────────────────┘             └──────────────────────┘             └──────────────────────┘
```

- **agent/** — Go binary chạy trên máy nhân viên (user-mode, không cần admin)
- **backend/** — Go REST API + PostgreSQL, deploy được mọi nơi (VPS, Docker, Cloud Run)
- **dashboard/** — Next.js admin dashboard (static export, deploy được Vercel/VPS/S3)
- **installer/** — Velopack installer 1-click cho nhân viên cài
- **infrastructure/** — Docker compose, nginx config, deploy scripts

## Nguyên tắc thiết kế

1. **User-mode only** — không request UAC, cài trong `%LOCALAPPDATA%`
2. **Privacy-first** — chỉ track online/offline + work time, KHÔNG screenshot/keylog
3. **Vendor-portable** — code chạy được trên mọi cloud provider, migrate trong 24h
4. **Defense in depth** — 3 layer (AI client / Agent / Watchdog) độc lập, self-healing
5. **Production-grade** — TypeScript strict, Go linting, structured logging, automated tests
6. **Code signing** — EV cert, Microsoft Defender submission tự động

## Quick Start (developer)

```bash
# 1. Setup environment
cp .env.example .env
# Sửa các giá trị trong .env

# 2. Start local stack
cd infrastructure
docker-compose up -d

# 3. Run backend
cd ../backend
make dev

# 4. Run dashboard
cd ../dashboard
npm install
npm run dev

# 5. Build agent for testing
cd ../agent
make build-windows
```

## Tài liệu

- [ARCHITECTURE.md](./ARCHITECTURE.md) — Kiến trúc chi tiết
- [SECURITY.md](./SECURITY.md) — Chính sách bảo mật
- [DEPLOYMENT.md](./DEPLOYMENT.md) — Hướng dẫn deploy production
- [RUNBOOK.md](./RUNBOOK.md) — Operational runbook (xử lý sự cố)
- [docs/](./docs/) — Documentation chi tiết

## License

Proprietary — Internal use only.
