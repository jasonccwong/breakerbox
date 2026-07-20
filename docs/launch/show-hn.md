# Show HN draft (Jason posts this himself)

**Title:**
Show HN: BreakerBox – on/off switches, metrics, and LLM token spend for self-hosted apps

**URL:** https://github.com/jasonccwong/breakerbox

**Text:**

I kept juggling three tools for the apps running on my Mac and a cheap VPS:
something to start/stop them (pm2/systemd), something to watch them (Beszel),
and something to track what Claude Code was costing me (ccusage). BreakerBox
is those three panes merged into one, with first-party mobile.

How it works: a single-binary hub (Go + PocketBase — embedded SQLite, auth,
realtime in one process) and a small agent per machine that dials out over
WebSocket, so home machines behind NAT need zero network config. Each app is
a card with an on/off switch, live CPU/mem/ports, log streaming, and
crash-restart policies. macOS, Linux, and Windows hosts are peers — native
processes and Docker containers both.

The unusual part is token tracking. The agent tails the transcript files
Claude Code and Codex already write locally, dedupes streaming rewrites,
prices rows against the LiteLLM table, and attributes spend to apps by
working directory — so the dashboard shows "this app cost $12.40 to build
this week" next to its CPU graph, zero config. For runtime API spend there's
an opt-in local proxy the agent wires in via ANTHROPIC_BASE_URL/
OPENAI_BASE_URL env injection (plain forwarding, no TLS interception).

Security design I want scrutiny on: agents authenticate with per-machine
Ed25519 keys (no shared secrets), and the hub can only issue a fixed verb set
(start/stop/restart/logs) against app definitions that were approved on the
host itself — a compromised hub can't push arbitrary commands. This is a
deliberate inversion of the design that got Nezha abused as a RAT.

MIT licensed, Go + React + Expo. Alpha: expect rough edges. I'd especially
value feedback on the host-side approval UX and the token attribution model.
