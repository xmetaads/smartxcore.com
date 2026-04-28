# WorkTrack — Operational Runbook

Người vận hành: **1 admin solo**. Tài liệu này được viết để bất kỳ ai (kể cả người mới tiếp quản) có thể xử lý sự cố trong 30 phút đọc.

---

## 1. Quy trình hàng ngày (Daily routine)

| Thời điểm | Việc |
|---|---|
| Sáng | Mở dashboard `/dashboard`. Kiểm tra số máy online vs hôm qua |
| Sáng | Xem inbox alert email (offline > 24h) |
| Bất kỳ lúc nào | Vào VirusTotal monitor để xem agent có bị flag không |
| Thứ 2 | Verify backup database tuần trước OK |
| Cuối tháng | Xem chi phí cloud, đảm bảo không vượt budget |

## 2. Cấu hình ban đầu (One-time setup)

### 2.1. Domain + DNS

```
api.<your-domain>          → Backend (Cloudflare proxied)
track.<your-domain>        → Dashboard (Vercel)
cdn.<your-domain>          → R2 bucket (installer + python.zip)
```

### 2.2. EV Code Signing

1. Mua từ DigiCert hoặc Sectigo (~$400/năm)
2. Verify công ty (nhân viên gọi xác minh)
3. Nhận hardware token (USB)
4. Cấu hình GitHub Actions secret: `EV_CERT_TOKEN`

### 2.3. Microsoft Defender Submission

URL: https://www.microsoft.com/en-us/wdsi/filesubmission

Submit mỗi version mới. Đính kèm:
- Binary + SHA256
- Mô tả: "Internal RMM agent for company X. Tracks online status and runs PowerShell. No malware behavior."

## 3. Xử lý sự cố thường gặp

### 3.1. Một máy offline > 24h

**Bước 1**: Kiểm tra dashboard `/machines/<id>`
- Last seen khi nào?
- Last public IP?
- Last events?

**Bước 2**: Liên hệ nhân viên
- Máy có bật không?
- Có internet không?
- Có cài lại Windows gần đây không?

**Bước 3**: Nếu agent bị xóa, gửi installer mới

### 3.2. Defender báo agent là malware

**Triệu chứng**: VirusTotal scan có engine mới flag agent.exe

**Bước 1**: Xác nhận false positive
- Re-scan trên VT
- Test agent trong sandbox

**Bước 2**: Submit lại Microsoft + AV vendor đó
- https://www.microsoft.com/en-us/wdsi/filesubmission
- Mỗi vendor có form riêng

**Bước 3**: Trong khi đợi (24-72h)
- Pause auto-update agent
- Theo dõi máy bị wipe agent qua dashboard
- Watchdog sẽ tự reinstall sau 10 phút mỗi máy

**Bước 4**: Khi MS confirm clean
- Resume auto-update
- Push version mới với hash khác

### 3.3. Backend không phản hồi

**Triệu chứng**: Tất cả máy hiển thị offline đột ngột

**Bước 1**: Check dashboard `/healthz` và `/readyz`

**Bước 2**: Check provider status page
- Cloudflare: https://www.cloudflarestatus.com
- Hosting VPS: provider's status page

**Bước 3**: SSH vào VPS, check logs
```
sudo journalctl -u worktrack-backend -n 100
```

**Bước 4**: Nếu DB issue
```
sudo systemctl status postgresql
psql -U worktrack -c "SELECT 1"
```

### 3.4. Database disk full

**Trigger**: Alert email "DB disk > 80%"

**Bước 1**: SSH vào DB host
```
df -h
du -sh /var/lib/postgresql/16/main/*
```

**Bước 2**: Drop partitions cũ
```sql
DROP TABLE IF EXISTS heartbeats_2025_01;  -- oldest month
DROP TABLE IF EXISTS events_2025_01;
```

**Bước 3**: Vacuum
```sql
VACUUM ANALYZE;
```

### 3.5. EV cert sắp hết hạn

**Trigger**: Alert 30 ngày trước expiry

**Bước 1**: Renew với DigiCert/Sectigo (mất 1-3 ngày)

**Bước 2**: Update GitHub Actions secret

**Bước 3**: Build agent version mới với cert mới

**Bước 4**: Submit Microsoft Defender với cert mới

## 4. Disaster recovery

### 4.1. Mất hoàn toàn provider chính

**Scenario**: VPS bị xóa, dashboard host bị khóa

**RTO**: 4 giờ. **Steps**:

1. Tải backup mới nhất từ R2 (database dump)
2. Mua VPS mới (Hetzner / DigitalOcean)
3. Cài Postgres + restore dump
4. Build & deploy backend lên VPS mới
5. Update DNS records → trỏ về IP mới
6. Đợi DNS propagate (15-60 phút)
7. Agents tự reconnect trong 5 phút sau DNS update

### 4.2. Mất tất cả 2 layer (EV cert + DB backup cùng lúc)

Cực kỳ hiếm nhưng cần plan:
- DB backup được lưu ở 2 region (R2 + S3 mirror)
- EV cert có recovery via DigiCert (cần verify lại)
- Xác suất xảy ra cùng lúc: rất thấp

## 5. Monitor & Alert

### 5.1. Dashboard health check (manual)

Mỗi sáng kiểm tra:
- `/dashboard` load OK
- Số máy online ~ngày trước
- Không có alert critical mở

### 5.2. Alerts được gửi qua email

| Alert | Severity | Action |
|---|---|---|
| Backend down > 5 phút | CRITICAL | Xử lý ngay |
| DB connection > 80% | CRITICAL | Tăng pool size |
| DB disk > 80% | CRITICAL | Drop partitions |
| Máy offline > 24h | WARNING | Liên hệ nhân viên |
| Defender flag (VT) | CRITICAL | Submit lại |
| EV cert < 30 ngày | WARNING | Renew |
| Heartbeat rate giảm > 20% | WARNING | Investigate |

## 6. Liên hệ khẩn cấp

| | |
|---|---|
| Hosting provider support | (điền sau) |
| DigiCert support | https://www.digicert.com/support/ |
| Cloudflare support | https://www.cloudflare.com/support/ |
| Microsoft Defender team | https://www.microsoft.com/wdsi/filesubmission |

## 7. Checklist khi tiếp quản (handover)

Người mới tiếp quản phải có:
- [ ] Access GitHub repo
- [ ] Access cloud provider (Cloudflare, hosting)
- [ ] Access DB credentials
- [ ] Access JWT_SECRET
- [ ] Access EV cert + hardware token
- [ ] Access email alert account
- [ ] Đã đọc ARCHITECTURE.md
- [ ] Đã đọc RUNBOOK.md (file này)
- [ ] Đã practice disaster recovery 1 lần
