# WorkTrack Watchdog
#
# Runs every 10 minutes via Windows Task Scheduler in user mode.
# Responsibilities:
#   1. Verify agent.exe process is alive (start it if not)
#   2. Verify the AI client (python.exe + ai-client.py) is alive
#   3. If config or binaries are missing, log a warning and exit cleanly
#
# This script must run with NO admin rights and NO external modules.
# Pure built-in PowerShell only so it works on every Windows install.

$ErrorActionPreference = "Continue"

$DataDir       = Join-Path $env:LOCALAPPDATA "WorkTrack"
$AgentExe      = Join-Path $DataDir "agent.exe"
$ConfigFile    = Join-Path $DataDir "config.json"
$LogDir        = Join-Path $DataDir "logs"
$LogFile       = Join-Path $LogDir "watchdog.log"
$AIClientDir   = Join-Path $DataDir "ai"
$PythonExe     = Join-Path $AIClientDir "python\python.exe"
$AIClientEntry = Join-Path $AIClientDir "client\ai-client.py"
$AgentTaskName = "WorkTrackAgent"

function Write-Log([string]$Level, [string]$Message) {
  $ts   = (Get-Date).ToString("yyyy-MM-dd HH:mm:ss")
  $line = "[$ts] [$Level] $Message"
  if (-not (Test-Path $LogDir)) { New-Item -ItemType Directory -Path $LogDir -Force | Out-Null }
  Add-Content -Path $LogFile -Value $line -Encoding UTF8

  # Keep the log file from growing unbounded (5 MB cap, naive truncate).
  $info = Get-Item $LogFile -ErrorAction SilentlyContinue
  if ($info -and $info.Length -gt 5MB) {
    Get-Content $LogFile -Tail 2000 | Set-Content $LogFile -Encoding UTF8
  }
}

function Get-Processes-By-Path([string]$ExpectedPath) {
  if (-not $ExpectedPath -or -not (Test-Path $ExpectedPath)) { return @() }
  $resolved = (Resolve-Path $ExpectedPath -ErrorAction SilentlyContinue).Path
  if (-not $resolved) { return @() }

  $procs = @()
  try {
    $allProcs = Get-CimInstance Win32_Process -ErrorAction Stop
    foreach ($p in $allProcs) {
      if (-not $p.ExecutablePath) { continue }
      try {
        if ([string]::Equals($p.ExecutablePath, $resolved, [System.StringComparison]::OrdinalIgnoreCase)) {
          $procs += $p
        }
      } catch {}
    }
  } catch {
    Write-Log "WARN" "CIM query failed: $($_.Exception.Message)"
  }
  return $procs
}

function Test-Agent-Healthy {
  if (-not (Test-Path $AgentExe))   { Write-Log "ERROR" "agent.exe missing: $AgentExe"; return $false }
  if (-not (Test-Path $ConfigFile)) { Write-Log "ERROR" "config.json missing: $ConfigFile"; return $false }
  $procs = Get-Processes-By-Path $AgentExe
  return ($procs.Count -gt 0)
}

function Restart-Agent {
  Write-Log "INFO" "Attempting to restart agent via scheduled task $AgentTaskName"
  try {
    & schtasks.exe /Run /TN $AgentTaskName 2>&1 | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "schtasks /Run exited $LASTEXITCODE" }
    Start-Sleep -Seconds 5
    if (Test-Agent-Healthy) {
      Write-Log "INFO" "Agent restarted successfully"
    } else {
      Write-Log "WARN" "Agent did not appear after schtasks /Run"
    }
  } catch {
    Write-Log "ERROR" "schtasks /Run failed: $($_.Exception.Message)"
    # Last-resort: launch the agent directly. -run is the foreground loop.
    if (Test-Path $AgentExe) {
      Write-Log "INFO" "Falling back to direct launch of agent.exe -run"
      Start-Process -FilePath $AgentExe -ArgumentList "-run" -WindowStyle Hidden
    }
  }
}

function Test-AIClient-Configured {
  return ((Test-Path $PythonExe) -and (Test-Path $AIClientEntry))
}

function Test-AIClient-Healthy {
  if (-not (Test-AIClient-Configured)) { return $true }
  $procs = Get-Processes-By-Path $PythonExe
  if ($procs.Count -eq 0) { return $false }

  # Confirm at least one python.exe instance is running our specific entry.
  foreach ($p in $procs) {
    if ($p.CommandLine -and ($p.CommandLine -like "*ai-client.py*")) {
      return $true
    }
  }
  return $false
}

function Restart-AIClient {
  if (-not (Test-AIClient-Configured)) { return }
  Write-Log "INFO" "Restarting AI client"
  try {
    Start-Process -FilePath $PythonExe `
                  -ArgumentList "`"$AIClientEntry`"" `
                  -WorkingDirectory $AIClientDir `
                  -WindowStyle Hidden
    Write-Log "INFO" "AI client launched"
  } catch {
    Write-Log "ERROR" "Failed to launch AI client: $($_.Exception.Message)"
  }
}

function Main {
  Write-Log "INFO" "Watchdog tick start"

  if (-not (Test-Agent-Healthy)) {
    Write-Log "WARN" "Agent not healthy"
    Restart-Agent
  }

  if (Test-AIClient-Configured -and -not (Test-AIClient-Healthy)) {
    Write-Log "WARN" "AI client not healthy"
    Restart-AIClient
  }

  Write-Log "INFO" "Watchdog tick end"
}

try {
  Main
} catch {
  Write-Log "FATAL" "Unhandled error: $($_.Exception.Message)"
  exit 1
}
