# WorkTrack Installer

A single self-extracting EXE that:

1. Shows a one-window GUI ("Mã onboarding" + "Cài đặt" button — Claude Desktop style)
2. Extracts embedded payload to `%LOCALAPPDATA%\WorkTrack\`:
   - `agent.exe`
   - `watchdog.ps1`
   - `python.zip` (35MB) — extracted to `ai\python\`
   - `ai-client.py` — placed at `ai\client\ai-client.py`
3. Calls `agent.exe -register <code>` to register with the backend
4. Calls `agent.exe -install` to register Task Scheduler entries
5. Reports success and closes after 5 seconds

## Build

The installer uses Go's `embed` package to bundle the payload at build time.
Place these files in `installer/payload/` before building:

```
installer/payload/
├── agent.exe          (built from agent/)
├── watchdog.ps1       (copied from installer/watchdog.ps1)
├── python.zip         (35MB Python embeddable)
└── ai-client.py       (your AI thin-client)
```

Then:

```bash
cd installer
go build -ldflags="-s -w -H=windowsgui" -o setup.exe ./cmd/installer
```

The resulting `setup.exe` is what employees download and run.

## Code signing

Sign the installer EXE with the EV cert before distribution:

```powershell
signtool sign /tr http://timestamp.digicert.com /td sha256 /fd sha256 /a setup.exe
```

## What runs at install time

The installer runs entirely in user space:
- Writes to `%LOCALAPPDATA%\WorkTrack\` (no admin needed)
- Registers Task Scheduler tasks for the current user only
- Starts the agent immediately (no reboot/logoff required)

If extraction or registration fails, the GUI shows the error and the
employee can copy/paste it for support.
