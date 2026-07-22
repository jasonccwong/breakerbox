// Generates the copy-paste prompt for Claude Code / Codex that produces a
// breakerbox.app.json in the user's project. The schema is embedded inline so
// the coding agent needs no network access to follow it.

export function generateAppDefPrompt(os: string): string {
  return `Analyze this project and create a BreakerBox app definition file so this app can be supervised (started/stopped/monitored) by a control panel.

Write a file named \`breakerbox.app.json\` in the project root, conforming to this JSON schema (schema_version 1):

{
  "schema_version": 1,               // required, literal 1
  "name": "<short-app-name>",        // required
  "kind": "process",                 // "process" for a native command
  "cmd": "<executable>",             // required: the binary/interpreter to run (absolute path or on PATH)
  "args": ["..."],                   // arguments array
  "cwd": "<absolute project path>",  // absolute path to run in (this directory)
  "env": {"KEY": "value"},           // only env vars the app truly needs
  "ports": [3000],                   // ports the app listens on
  "health_check": {                  // optional: HTTP liveness probe
    "url": "http://localhost:3000/", "timeout_s": 5
  },
  "stop": {                          // how to stop gracefully
    "signal": "SIGTERM",             // signal sent to the process group
    "timeout_s": 10                  // seconds before SIGKILL escalation
  },
  "restart_policy": {"max_restarts": 15, "min_uptime_s": 1, "backoff_max_s": 60}
}

Requirements:
1. Inspect the project (package.json scripts, Procfile, pyproject/requirements, docker-compose, Makefile, README) to determine the PRODUCTION start command — not the dev/watch command if a production one exists (e.g. prefer "npm run start" or a built binary over "npm run dev"; for Python prefer gunicorn/uvicorn invocations if configured).
2. The command must run in the FOREGROUND (no daemonizing flags, no "&", no nohup) — the supervisor manages backgrounding.
3. Use the absolute path of this project directory for "cwd".
3b. CRITICAL: the agent runs as a background service with a MINIMAL PATH
   (launchd/systemd defaults — no homebrew, no nvm, no ~/.local/bin). So:
   use the ABSOLUTE path for "cmd" (resolve it now with \`which <cmd>\`), and
   set "env.PATH" to a colon-joined PATH that includes the directories of
   every tool the command needs at runtime (e.g. node for npm scripts) plus
   the system defaults (/usr/bin:/bin:/usr/sbin:/sbin).
4. Target OS: ${os}. Use paths/executables valid on it.
5. If the app needs a build step before it can start, run that build now so the start command works immediately.
6. List every port the app listens on; add a health_check URL if the app serves HTTP.
7. Do NOT include secrets in "env" — reference the app's own .env loading instead.
8. Validate that your JSON parses. Output nothing else to the file.

After writing the file, verify the start command actually works by running it briefly in the foreground, then stopping it. Finally print exactly this line so the user knows the next step:

  Import it with: breakerbox-agent apps import breakerbox.app.json`;
}
