# BreakerBox

**One panel of switches for every app you self-host — with the metrics and LLM token spend to match.**

BreakerBox is a lightweight, open-source control panel for the apps you run on your own machines — your Mac, your Windows PC, your Linux VPS — all as peers in one dashboard. Every app gets a first-class **on/off switch**, live status, CPU/memory/port readouts, and (uniquely) its **LLM token usage**, whether that's the Claude Code sessions that built it or the API calls it makes at runtime.

Think **Beszel + Dockge + ccusage in one pane**.

> ⚠️ **Status: alpha, under active development.** Hub + agent (macOS & Linux) + web dashboard work end-to-end today: app on/off switches, native **and Docker/Compose** apps, crash-restart policies, resurrect-on-boot, live log streaming, metric history, and ntfy push alerts. Token tracking and mobile land next. Nothing is stable yet — star/watch to follow along.

## Why another panel?

Every existing tool does part of this, none does all of it:

| | App on/off | Host & app metrics | Token usage | Your Mac/PC as a host | Open source |
|---|---|---|---|---|---|
| Coolify / Dokploy | ✅ (Linux servers) | basic | ❌ | ❌ ("not planned") | ✅ |
| Beszel / Netdata / Uptime Kuma | ❌ observe-only | ✅ | ❌ | partial | ✅ |
| PM2 + pm2.io | ✅ | ✅ | ❌ | ✅ | dashboard paywalled |
| ccusage & friends | ❌ | ❌ | ✅ | ✅ | ✅ |
| **BreakerBox** | ✅ | ✅ | ✅ | ✅ | ✅ MIT |

## Design principles

- **One binary each.** The hub is a single binary (embedded SQLite, auth, web UI included). The agent is a single static binary per OS. No Postgres, no Prometheus, no YAML.
- **Agents dial out.** Works behind NAT with zero port forwarding. Hub on a $4 VPS or your home machine.
- **No shell, ever.** The hub can only ask an agent to `start` / `stop` / `restart` / `status` / `logs` for apps you registered **and approved on the host**. There is no remote command execution, no PTY, no file access in the protocol. A compromised hub cannot run code on your machines. See [SECURITY.md](SECURITY.md).
- **Your coding agent writes the config.** BreakerBox generates a prompt; you run it with Claude Code or Codex inside your project; it writes `breakerbox.app.json` with the right start/stop procedure; you import it. Zero config-file authoring by hand.

## Architecture

```
┌─────────┐   ┌─────────┐        ┌──────────────────────────┐
│ Web SPA │   │ Mobile  │  HTTPS │           Hub            │
│(embedded│   │ (Expo)  │───────▶│  Go + PocketBase: auth,  │
│ in hub) │   │         │        │  SQLite, realtime, API   │
└─────────┘   └─────────┘        └─────────▲────────────────┘
                                           │ outbound WebSocket
                                           │ (Ed25519 per-agent identity)
                        ┌──────────────────┼──────────────────┐
                  ┌─────┴─────┐      ┌─────┴─────┐      ┌─────┴─────┐
                  │   Agent   │      │   Agent   │      │   Agent   │
                  │ (your Mac)│      │(Linux VPS)│      │(Windows PC)│
                  └───────────┘      └───────────┘      └───────────┘
                   supervises apps · collects metrics · tails token logs
```

## Quick start

Hub (any always-on box — VPS, home server, or the Mac you're on):

```sh
curl -fsSL https://raw.githubusercontent.com/jasonccwong/breakerbox/main/scripts/install-hub.sh | sh
```

Open `http://localhost:8090`, create your account, click **+ Add system**, and
paste the generated command on each machine you want on the panel:

```sh
curl -fsSL https://raw.githubusercontent.com/jasonccwong/breakerbox/main/scripts/install-agent.sh \
  | sh -s -- --hub https://your-hub --token <ENROLL_TOKEN>
```

Then **+ Add app** → let Claude Code/Codex generate the app definition → approve
it on the host → flip the switch. Hub at home? See [docs/home-hub.md](docs/home-hub.md)
for Cloudflare Tunnel / Tailscale setups.

## Roadmap

- ~~**Phase 1:** macOS agent + hub + web dashboard walking skeleton — enroll, register an app, flip the switch, watch live CPU/mem~~ ✅
- ~~**Phase 2:** Linux/VPS support, Docker & Compose apps, restart policies, log viewer, install scripts, ntfy alerts~~ ✅ — first public alpha
- **Phase 3 (next):** Token tracking (Claude Code + Codex), mobile app (iOS/Android)
- **Phase 4:** Windows agent, runtime token proxy, v0.1.0

## Development

Requires Go 1.26+, Node 20+, pnpm.

```sh
make dev      # run hub with live web proxy
make test     # all Go module tests
make agent    # build the agent binary
```

## License

[MIT](LICENSE)
