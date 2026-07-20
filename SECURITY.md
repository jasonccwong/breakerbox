# Security Model

Remote control of machines is the entire product — so the design starts from the question: **what happens when the hub is compromised?**

Monitoring tools with control planes have been weaponized before (a well-known agent/dashboard tool became a favorite RAT precisely because it offered shared-secret auth, arbitrary shell execution, and root agents). BreakerBox inverts every one of those choices:

## Per-agent identity, never shared secrets

Each agent generates its own Ed25519 keypair at enrollment; the private key never leaves the host. Agents authenticate every connection by signing a timestamped challenge (±5 min skew window). Revoking one host = deleting one record. There is no shared cluster secret to leak, and no default credentials anywhere.

## App-scoped verbs, no shell

The complete set of actions a hub can request from an agent:

- `start` / `stop` / `restart` a **pre-registered app**
- report `status` and stream `logs` for a **pre-registered app**

There is deliberately **no** remote command execution, no PTY/terminal, no file upload/download, and no way to add these via configuration. They are absent from the wire protocol itself.

## Host-side approval

An agent will not execute an app definition whose hash it has not **locally approved** (`breakerbox-agent apps approve <id>` on the host, which prints the exact command, working directory, and environment first). Editing a definition in the UI invalidates the approval until re-approved on the host. Consequence: even with full control of the hub, an attacker cannot make your machines run a command you didn't explicitly approve on the machine itself.

## Least privilege

The agent runs as a regular service account, not root/SYSTEM. It needs no inbound ports (it dials out to the hub over WebSocket/TLS).

## Reporting a vulnerability

Please report suspected vulnerabilities privately via GitHub Security Advisories ("Report a vulnerability" on the repo). We'll acknowledge within 72 hours. Please do not open public issues for security reports.
