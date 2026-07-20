# r/selfhosted draft (Jason posts this himself)

**Title:**
BreakerBox – Beszel + Dockge + ccusage in one pane: switches, metrics, and
LLM token spend for your apps (open source, MIT)

**Body:**

I love Beszel for metrics and Dockge for compose stacks, but I kept wishing
for one screen where every app — native process on my Mac, container on my
VPS — is just a switch with its vitals next to it. So I built BreakerBox.

**What it does**

- Every app gets an on/off switch, status, CPU/mem, listening ports, and
  streamed logs — native processes AND Docker/Compose, on macOS, Linux, and
  Windows hosts, all peers in one dashboard
- Crash-restart policies (min-uptime/backoff like pm2), resurrect-on-boot
  with orphan reaping, metric history with Beszel-style downsampling
- ntfy alerts: app crash-looping, host offline, daily LLM spend threshold
- Mobile app (Expo, iOS/Android) that connects straight to your hub
- The odd one out: it tails Claude Code/Codex transcript files and shows what
  each app costs in tokens next to its CPU graph — zero config

**What it deliberately doesn't do**

No deployments, no builds, no reverse proxy, no app store — Coolify and
friends own that. This is the "operate what I already run" layer.

**Setup**

Hub is one binary (PocketBase-style, SQLite inside). Agents dial out over
WebSocket, so nothing to port-forward at home; docs cover Cloudflare
Tunnel/Tailscale if the hub lives on your LAN. Agents hold per-machine
Ed25519 keys and only accept a fixed verb set against definitions you
approved on the host — the hub can never push a shell command.

MIT, Go backend, alpha status — issues and PRs very welcome.
https://github.com/jasonccwong/breakerbox
