# WorkTrack Dashboard

Admin console (Next.js 15 + TypeScript + Tailwind + shadcn/ui) for the WorkTrack RMM system.

## Setup

```bash
npm install
cp .env.example .env.local
# Edit .env.local with your API base URL

npm run dev
```

Open http://localhost:3000

## Build

```bash
npm run build
npm run start
```

## Deploy options

This dashboard is deliberately portable:

- **Vercel** — push to GitHub, connect repo
- **Cloudflare Pages** — same flow
- **VPS with nginx** — `npm run build` then serve `out/` directory
- **Docker** — see `infrastructure/docker/Dockerfile.dashboard`

No vendor-specific features used.

## Routes

| Route | Purpose |
|---|---|
| `/` | Landing — choose admin login or employee onboarding |
| `/login` | Admin login |
| `/onboarding` | Employee enters onboarding code, downloads installer |
| `/dashboard` | Overview (online count, alerts) |
| `/machines` | List of all machines + filter |
| `/commands` | PowerShell remote console |
| `/reports` | Work time and uptime reports |
| `/settings` | System config |

## Stack notes

- Auth uses HTTP-only cookies set by the Go backend (no Clerk lock-in).
- API calls go through `src/lib/api.ts` — single place to swap URL.
- All UI text is in Vietnamese; technical strings in English.
