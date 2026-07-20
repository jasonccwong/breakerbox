# Token tracking

BreakerBox tracks two kinds of LLM spend and shows both on the **Tokens**
screen (web and mobile), stacked by day and model, broken down per app.

## Dev-time spend (zero configuration)

The agent tails the transcript files your coding agents already write:

| Tool | Source | Attribution |
|---|---|---|
| Claude Code | `~/.claude/projects/**/*.jsonl` | working directory → app `cwd` |
| Codex CLI | `~/.codex/sessions/**/*.jsonl` | session cwd → app `cwd` |

Nothing to configure: usage rows are deduplicated (streaming rewrites of the
same message update in place), priced against the community-maintained
LiteLLM price table, and matched to your registered apps by longest
working-directory prefix. Sessions in directories that aren't a registered
app appear under **Unattributed** — visible, never silently dropped.

Notes:
- Costs are estimates computed from token counts × published prices. For
  subscription plans (Claude Pro/Max) this is "what it would have cost",
  useful as a consumption gauge.
- The pricing table ships with each release; **Settings → Refresh pricing**
  (or `POST /api/bb/pricing/refresh`) pulls the latest from LiteLLM.
- Unknown models are recorded with cost 0 and flagged in the hub log.

## Runtime spend (opt-in per app)

For apps that call Anthropic/OpenAI APIs at runtime, enable **Runtime API
metering** on the app's detail page. On next start the agent injects:

```
ANTHROPIC_BASE_URL=http://127.0.0.1:<port>/t/<app>/anthropic
OPENAI_BASE_URL=http://127.0.0.1:<port>/t/<app>/openai/v1
```

Official SDKs honor these automatically — no code changes. The agent's local
proxy forwards traffic to the real provider over normal TLS (**no MITM, no
certificates**) and reads usage from responses, including streaming (it
forces `stream_options.include_usage` on OpenAI streams). Rows appear under
the app with source `runtime_proxy`.

Limitations, stated plainly:
- Apps that ignore the base-URL env vars are not covered.
- Process apps only; containers manage their own environment.
- The proxy listens on localhost only, but any local process could send
  traffic through it; rows are attributed by URL path, not authenticated.

## Spend alerts

**Settings → Notifications**: set a daily USD threshold; when the day's total
crosses it, one ntfy push fires (at most once per day).
