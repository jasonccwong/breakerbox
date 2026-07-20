# BreakerBox on Windows

The agent runs natively on Windows 10/11 and Windows Server (amd64/arm64) as
a Windows Service.

## Install

From an **elevated** PowerShell, using the enroll token from your hub's
**+ Add system** dialog:

```powershell
irm https://raw.githubusercontent.com/jasonccwong/breakerbox/main/scripts/install-agent.ps1 -OutFile install-agent.ps1
.\install-agent.ps1 -Hub https://your-hub -Token <ENROLL_TOKEN>
```

This installs `breakerbox-agent.exe` to `C:\Program Files\BreakerBox`,
enrolls the machine, and registers the `breakerbox-agent` service
(auto-start, restart-on-failure). State lives in
`%ProgramData%\breakerbox-agent`; logs go to **Event Viewer → Windows Logs →
Application** (source `breakerbox-agent`).

## Managing apps

Everything works as on macOS/Linux — approve app definitions on the host:

```powershell
& "C:\Program Files\BreakerBox\breakerbox-agent.exe" apps list
& "C:\Program Files\BreakerBox\breakerbox-agent.exe" apps approve <APP_ID>
```

## Platform notes (please read)

- **Process trees are managed with Job Objects** — stopping an app reliably
  kills the whole tree, including grandchildren.
- **There is no graceful stop on Windows.** Windows has no cross-process
  POSIX signals, so `stop.signal` is ignored and stop is always an immediate
  tree kill. Apps that need graceful shutdown should handle it via their own
  mechanisms (HTTP shutdown endpoints, etc.).
- **Session 0**: services run in a non-interactive session, so supervised GUI
  apps won't display windows. BreakerBox targets server-style workloads
  (web apps, APIs, workers) on Windows.
- **Docker**: container apps require Docker Desktop with the WSL2 backend;
  the agent talks to the engine through the named-pipe-mounted socket only if
  `DOCKER_HOST` points at a unix socket inside WSL. Native Windows container
  management is untested in this release.
- **Token tracking** works the same as elsewhere: Claude Code transcripts are
  read from `%USERPROFILE%\.claude`, Codex from `%USERPROFILE%\.codex`.
