# Kiến trúc hệ thống WorkTrack

Tài liệu này mô tả chi tiết kiến trúc kỹ thuật. Đối tượng đọc: developer maintain hệ thống và admin vận hành.

---

## 1. Tổng quan

WorkTrack là hệ thống RMM tự xây cho công ty 2,000 nhân viên Windows toàn cầu, gồm 3 thành phần độc lập:

```
                     INTERNET
                         │
        ┌────────────────┼─────────────────────┐
        │                │                     │
        ▼                ▼                     ▼
┌──────────────┐  ┌──────────────┐   ┌──────────────────┐
│ Employee PCs │  │   Backend    │   │  Admin Dashboard │
│  (Agent.exe) │  │  api.xxx.com │   │  track.xxx.com   │
└──────┬───────┘  └──────┬───────┘   └─────────┬────────┘
       │                 │                     │
       │ POST heartbeat  │  CRUD via REST      │
       │ POST events     │                     │
       │ GET commands    │                     │
       └────────┬────────┘                     │
                │                              │
                ▼                              │
        ┌──────────────┐                       │
        │  PostgreSQL  │ ◀─────────────────────┘
        │  (managed)   │
        └──────────────┘
```

## 2. Thành phần Agent (Go binary)

### 2.1. Vị trí cài đặt (user-mode)

| Loại | Đường dẫn |
|---|---|
| Binary | `%LOCALAPPDATA%\WorkTrack\agent.exe` |
| Config | `%LOCALAPPDATA%\WorkTrack\config.json` |
| Logs | `%LOCALAPPDATA%\WorkTrack\logs\agent.log` |
| Buffer | `%LOCALAPPDATA%\WorkTrack\buffer\` (offline events) |
| Watchdog | `%LOCALAPPDATA%\WorkTrack\watchdog.ps1` |

### 2.2. Tự khởi động (không cần admin)

- **User Task Scheduler** — trigger logon, restart on failure
- **HKCU\Software\Microsoft\Windows\CurrentVersion\Run** — backup persistence
- KHÔNG dùng Windows Service (cần admin)

### 2.3. Module nội bộ

```
agent/internal/
├── config/      Đọc config.json, env vars
├── eventlog/    Đọc Windows Event Log (logon/logoff/boot/shutdown)
├── heartbeat/   Gửi heartbeat HTTP mỗi 60s
├── command/     Polling commands từ server, thực thi PowerShell
├── service/     Tích hợp User Task Scheduler
├── api/         HTTP client (mTLS, retry, exponential backoff)
└── logger/      Structured JSON logging
```

### 2.4. Event Log codes được track

| Event ID | Source | Ý nghĩa |
|---|---|---|
| 6005 | EventLog | Boot up |
| 6006 | EventLog | Shutdown |
| 7001 | Winlogon | User logon |
| 7002 | Winlogon | User logoff |
| 4624 | Security | Successful logon |
| 4634 | Security | Logoff |
| 4800 | Security | Workstation locked |
| 4801 | Security | Workstation unlocked |

### 2.5. Resilience (3 layer defense)

```
Layer 1: AI Client (độc lập, không qua Agent)
Layer 2: Agent (tracking + remote PS)
Layer 3: Watchdog (PowerShell, kiểm tra Agent + AI Client)
```

Watchdog chạy mỗi 10 phút qua User Task Scheduler:
1. Kiểm tra `agent.exe` có process không
2. Nếu không → tải lại từ R2 + reinstall
3. Kiểm tra Python process có chạy không
4. Nếu không → restart AI client
5. Báo về backend qua HTTPS heartbeat của riêng nó

## 3. Thành phần Backend (Go API)

### 3.1. Tech stack

| Layer | Choice | Lý do |
|---|---|---|
| Language | Go 1.22+ | Performance, single binary, portable |
| HTTP framework | Fiber v2 | Fast, Express-like, hỗ trợ middleware |
| Database driver | pgx v5 | Postgres native, performance tốt |
| Migrations | golang-migrate | Standard, version-controlled |
| Auth | Lucia-go (custom) | Self-hosted, không Clerk lock-in |
| Logging | zerolog | Structured JSON, performance |
| Validation | go-playground/validator | Standard |
| Config | viper | Multi-source config (env, file) |

### 3.2. API endpoints

```
# Public (agent endpoints)
POST   /api/v1/agent/register          - Đăng ký agent mới (qua onboarding token)
POST   /api/v1/agent/heartbeat         - Heartbeat mỗi 60s
POST   /api/v1/agent/events            - Gửi events batch
GET    /api/v1/agent/commands          - Polling commands (long-poll)
POST   /api/v1/agent/commands/:id/result - Trả kết quả PowerShell

# Admin (dashboard endpoints, JWT auth)
GET    /api/v1/admin/machines          - List 2000 máy
GET    /api/v1/admin/machines/:id      - Chi tiết 1 máy
POST   /api/v1/admin/commands          - Tạo command mới
GET    /api/v1/admin/reports/worktime  - Báo cáo work time
GET    /api/v1/admin/reports/uptime    - Báo cáo uptime
POST   /api/v1/admin/onboarding-tokens - Tạo token cho nhân viên mới
```

### 3.3. Database schema (PostgreSQL)

Xem [backend/migrations/001_initial.sql](./backend/migrations/001_initial.sql)

### 3.4. Deploy options (portable)

Backend là 1 Go binary, deploy được:
1. **VPS bare-metal** (Hetzner, DigitalOcean) — `systemctl` service
2. **Docker** — `docker run -d`
3. **Cloud Run** — serverless, autoscale
4. **AWS Lambda** — qua adapter
5. **Kubernetes** — Helm chart

Migrate giữa các option chỉ cần đổi config + restart.

## 4. Thành phần Dashboard (Next.js)

### 4.1. Tech stack

| Layer | Choice |
|---|---|
| Framework | Next.js 15 (App Router) |
| Language | TypeScript strict mode |
| Styling | Tailwind CSS |
| Components | shadcn/ui |
| State | TanStack Query + Zustand |
| Forms | React Hook Form + Zod |
| Charts | Recharts |
| Tables | TanStack Table |
| Auth | next-auth (self-hosted) |

### 4.2. Routes

```
/                          - Login page
/dashboard                 - Overview (online count, alerts)
/machines                  - List 2000 máy + filter
/machines/[id]             - Chi tiết 1 máy + history
/commands                  - PowerShell remote console
/reports                   - Báo cáo work time, uptime
/onboarding                - Tạo onboarding tokens cho nhân viên mới
/settings                  - Cấu hình hệ thống
```

### 4.3. Deploy options

Static export Next.js → HTML/CSS/JS thuần, deploy được:
1. **Vercel** (mặc định)
2. **Cloudflare Pages**
3. **VPS với nginx**
4. **AWS S3 + CloudFront**

## 5. Distribution & Installer

### 5.1. Velopack installer (1-click)

Inspired bởi Claude Desktop:
```
setup.exe (~50MB)
├── Bundle: agent.exe + python.zip + ai-client + watchdog.ps1
├── Click 1 lần → GUI loading → tự cài
├── Cài vào %LOCALAPPDATA%\WorkTrack\
├── Đăng ký Task Scheduler
├── Khởi động agent + AI client
└── Done (30 giây total)
```

### 5.2. Distribution channel

```
Onboarding portal (portal.xxx.com)
  ├── Nhân viên login bằng mã onboarding
  ├── Click "Tải Workspace App"
  ├── setup.exe download (signed EV)
  └── Run → cài tự động
```

### 5.3. Code signing pipeline

```
GitHub Actions (mỗi tag release):
  1. Build agent.exe (Go static)
  2. Sign EV với hardware token
  3. Upload to VirusTotal API check
  4. Submit to Microsoft Defender Portal
  5. Submit to AVs khác (Avast, Kaspersky, etc.)
  6. Đợi 24-72h verify clean
  7. Build setup.exe (Velopack) + sign
  8. Upload to R2 với signed URL
  9. Update version manifest
```

## 6. Bảo mật (security model)

### 6.1. Threat model

| Threat | Mitigation |
|---|---|
| Agent bị compromise | Token unique mỗi máy, scope giới hạn |
| MITM trên đường truyền | HTTPS + cert pinning |
| Replay attack | Timestamp + nonce trong request |
| SQL injection | Prepared statements (pgx) |
| XSS dashboard | React auto-escape + CSP headers |
| CSRF | SameSite cookies + CSRF token |
| Credential leak | Env vars, không hardcode, secret rotation |
| DDoS | Rate limiting per-IP + per-token |
| Malicious PowerShell | Audit log mọi command, signed scripts only |

### 6.2. Secrets management

```
Local dev:    .env file (gitignored)
Production:   Cloud provider secret manager
              (Cloudflare secrets / AWS SSM / HashiCorp Vault)
```

### 6.3. Audit logging

Mọi action admin (run command, create token, change config) được log:
- Ai làm (admin user ID)
- Lúc nào (timestamp)
- Trên máy nào (machine ID)
- Nội dung command đầy đủ
- Kết quả (success/fail + output)

## 7. Observability

### 7.1. Metrics (Prometheus)

```
worktrack_agents_online_total       - Số agent online
worktrack_heartbeats_received_total - Counter heartbeats
worktrack_commands_executed_total   - Counter PowerShell runs
worktrack_api_request_duration_seconds - Histogram latency
worktrack_db_connections_active     - Postgres connection pool
```

### 7.2. Logs (structured JSON)

Tất cả components log JSON structured để dễ search/filter:
```json
{"ts":"2026-04-28T10:00:00Z","level":"info","component":"agent","machine_id":"abc","msg":"heartbeat sent"}
```

### 7.3. Alerts (email)

| Trigger | Action |
|---|---|
| Máy offline > 24h | Email admin |
| Backend down > 5 phút | Email admin |
| Defender flag agent | Email admin (urgent) |
| Database disk > 80% | Email admin |
| EV cert expire < 30 ngày | Email admin |

## 8. Disaster Recovery

### 8.1. RTO/RPO targets

| | Target |
|---|---|
| RTO (Recovery Time Objective) | 4 giờ |
| RPO (Recovery Point Objective) | 1 giờ |

### 8.2. Backup strategy

| Component | Backup | Retention |
|---|---|---|
| Database | Daily pg_dump → R2 | 30 ngày |
| Database | WAL streaming | 24h |
| Agent binaries | GitHub Releases | Vĩnh viễn |
| Config | Git versioned | Vĩnh viễn |
| Secrets | Encrypted backup → 2 location | Vĩnh viễn |

### 8.3. Migration scenario

Nếu provider có vấn đề (Cloudflare/Vercel/Supabase down):
1. Restore database từ pg_dump (15 phút)
2. Deploy backend Go binary lên VPS khác (15 phút)
3. Update DNS A record (15 phút - 1h propagation)
4. Agent tự reconnect (≤5 phút sau DNS update)
5. **Total**: 1-2 giờ

## 9. Roadmap

| Phase | Mốc thời gian | Mục tiêu |
|---|---|---|
| Phase 0 | Tháng 0 | Foundation: domain, EV cert, repo, CI/CD |
| Phase 1 | Tháng 1 | MVP: agent + backend + dashboard cơ bản |
| Phase 2 | Tháng 2 | Hardening: signing, Defender submission, monitor |
| Phase 3 | Tháng 3 | Alpha pilot 5-10 máy |
| Phase 4 | Tháng 4 | Beta pilot 50-100 máy |
| Phase 5 | Tháng 5 | Scale 500 máy + parallel với NinjaOne |
| Phase 6 | Tháng 6 | Full rollout 2000 máy |
| Phase 7 | Tháng 7 | NinjaOne cutover |
