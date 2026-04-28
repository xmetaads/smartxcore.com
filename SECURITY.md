# Security Policy

## Threat Model

Hệ thống WorkTrack được thiết kế với các nguyên tắc bảo mật sau:

### Trust Boundaries

```
┌──────────────────────────────────────────────────────────────┐
│ UNTRUSTED                                                    │
│ - Internet (DDoS, MITM, malicious agents)                    │
└─────────────────────────────┬────────────────────────────────┘
                              │ HTTPS only (TLS 1.2+)
                              ▼
┌──────────────────────────────────────────────────────────────┐
│ DMZ                                                          │
│ - Cloudflare WAF                                             │
│ - Rate limiting per-IP                                       │
└─────────────────────────────┬────────────────────────────────┘
                              │ X-Agent-Token / JWT
                              ▼
┌──────────────────────────────────────────────────────────────┐
│ TRUSTED                                                      │
│ - Backend API (validate token first)                         │
│ - PostgreSQL (no public access)                              │
│ - Object storage (signed URLs only)                          │
└──────────────────────────────────────────────────────────────┘
```

### Threats & Mitigations

| Threat | Impact | Mitigation |
|---|---|---|
| Compromised agent | Leak 1 employee's tracking | Per-machine token, no shared secret |
| MITM | Intercept commands | TLS 1.2+, HSTS, cert pinning (agent) |
| Replay attack | Re-execute commands | Timestamp + nonce in signed requests |
| SQL injection | DB compromise | Prepared statements (pgx) |
| XSS dashboard | Steal admin session | React auto-escape + CSP headers |
| CSRF | Force admin actions | SameSite cookies + CSRF token |
| Token leak (logs) | Compromise machine | Tokens never logged, only IDs |
| Malicious PowerShell | Damage employee machine | Audit log, MFA on dashboard, signed scripts only |
| DDoS | Service disruption | Rate limiting, Cloudflare WAF |
| Privilege escalation | Get admin access | RBAC, principle of least privilege |
| Insider threat | Admin abuse | Full audit log, peer review for sensitive ops |

## Cryptography

| Use case | Algorithm |
|---|---|
| Password hashing | bcrypt cost=12 |
| JWT signing | HS256 (32+ char secret) |
| Agent token | 64 random bytes, base64url |
| TLS | TLS 1.2 minimum, TLS 1.3 preferred |
| Database encryption at rest | Provider-managed (LUKS/RDS) |

## Privacy

WorkTrack tracks **only** these data points:
- Online/offline status (heartbeat)
- Boot, shutdown, login, logout, lock, unlock events
- Hardware fingerprint (CPU, RAM, OS) on registration
- Public IP address

WorkTrack does **NOT** collect:
- Screenshots
- Keystrokes
- Browser history
- File contents
- Application usage
- Webcam, microphone, location
- Network packet contents

## Audit Logging

Every admin action is logged with:
- Admin user ID
- Timestamp
- IP address
- User agent
- Resource type/ID
- Full command content (for PowerShell ops)

Audit log is retained for **3 years** and read-only after write.

## Incident Response

Nếu nghi ngờ compromise:
1. Rotate JWT_SECRET (force all admins to re-login)
2. Disable affected agent tokens (UPDATE machines SET disabled_at = NOW())
3. Check audit_log for anomalous activity
4. Review CloudFlare/AWS access logs
5. Contact security@example.com

## Reporting

Security issues should be reported privately to: security@example.com

Do not file public issues for security vulnerabilities.
