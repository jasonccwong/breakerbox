import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { pb, sendCommand } from "./lib/pb";
import type { AppRecord, SystemRecord } from "./lib/pb";
import { StatusPill, Toggle, Modal, CopyBlock } from "./components/ui";
import { generateAppDefPrompt } from "./lib/prompt";

export default function App() {
  const nav = useNavigate();
  const qc = useQueryClient();
  const [enrollOpen, setEnrollOpen] = useState(false);
  const [addAppSystem, setAddAppSystem] = useState<SystemRecord | null>(null);

  useEffect(() => {
    if (!pb.authStore.isValid) nav("/login");
  }, [nav]);

  const { data: systems } = useQuery({
    queryKey: ["systems"],
    queryFn: () => pb.collection("systems").getFullList<SystemRecord>({ sort: "name" }),
  });
  const { data: apps } = useQuery({
    queryKey: ["apps"],
    queryFn: () => pb.collection("apps").getFullList<AppRecord>({ sort: "name" }),
  });

  // Realtime: any change to systems/apps invalidates the lists.
  useEffect(() => {
    const cols = ["systems", "apps"];
    cols.forEach((col) => {
      pb.collection(col)
        .subscribe("*", () => qc.invalidateQueries({ queryKey: [col] }))
        .catch(() => {});
    });
    return () => {
      cols.forEach((col) => pb.collection(col).unsubscribe("*").catch(() => {}));
    };
  }, [qc]);

  return (
    <div className="mx-auto max-w-5xl px-4 py-8">
      <header className="mb-8 flex items-center justify-between">
        <div className="flex items-center gap-2">
          <span className="text-2xl">⚡</span>
          <h1 className="text-xl font-bold tracking-tight">BreakerBox</h1>
        </div>
        <div className="flex items-center gap-3">
          <button
            onClick={() => setEnrollOpen(true)}
            className="rounded-lg bg-amber-500 px-3 py-1.5 text-sm font-semibold text-zinc-950 hover:bg-amber-400"
          >
            + Add system
          </button>
          <button
            onClick={() => {
              pb.authStore.clear();
              nav("/login");
            }}
            className="text-sm text-zinc-400 hover:text-zinc-200"
          >
            Sign out
          </button>
        </div>
      </header>

      {systems?.length === 0 && (
        <div className="rounded-xl border border-dashed border-zinc-800 p-10 text-center text-zinc-400">
          <p className="mb-3">No systems yet. Enroll your first machine:</p>
          <button
            onClick={() => setEnrollOpen(true)}
            className="rounded-lg bg-amber-500 px-4 py-2 text-sm font-semibold text-zinc-950 hover:bg-amber-400"
          >
            + Add system
          </button>
        </div>
      )}

      <div className="space-y-6">
        {systems?.map((sys) => (
          <SystemCard
            key={sys.id}
            system={sys}
            apps={(apps ?? []).filter((a) => a.system === sys.id)}
            onAddApp={() => setAddAppSystem(sys)}
          />
        ))}
      </div>

      {enrollOpen && <EnrollModal onClose={() => setEnrollOpen(false)} />}
      {addAppSystem && <AddAppModal system={addAppSystem} onClose={() => setAddAppSystem(null)} />}
    </div>
  );
}

function SystemCard({
  system,
  apps,
  onAddApp,
}: {
  system: SystemRecord;
  apps: AppRecord[];
  onAddApp: () => void;
}) {
  return (
    <section className="rounded-xl border border-zinc-800 bg-zinc-900/50">
      <div className="flex items-center justify-between border-b border-zinc-800 px-4 py-3">
        <div className="flex items-center gap-3">
          <StatusPill status={system.status} />
          <span className="font-semibold">{system.name}</span>
          <span className="text-xs text-zinc-500">
            {system.os}/{system.arch} {system.hostname && `· ${system.hostname}`}
          </span>
        </div>
        <button onClick={onAddApp} className="text-sm text-amber-400 hover:text-amber-300">
          + Add app
        </button>
      </div>
      {apps.length === 0 ? (
        <p className="px-4 py-6 text-sm text-zinc-500">No apps registered on this system.</p>
      ) : (
        <ul className="divide-y divide-zinc-800/60">
          {apps.map((app) => (
            <AppRow key={app.id} app={app} />
          ))}
        </ul>
      )}
    </section>
  );
}

function AppRow({ app }: { app: AppRecord }) {
  const [busy, setBusy] = useState(false);
  const running = app.status === "running" || app.status === "starting";

  async function toggle(next: boolean) {
    setBusy(true);
    try {
      await sendCommand(app, next ? "start" : "stop");
    } finally {
      // Realtime updates flip the visible state; a short delay avoids flicker.
      setTimeout(() => setBusy(false), 800);
    }
  }

  return (
    <li className="flex items-center justify-between gap-4 px-4 py-3">
      <div className="min-w-0">
        <Link to={`/apps/${app.id}`} className="font-medium hover:text-amber-400">
          {app.name}
        </Link>
        <div className="mt-0.5 flex items-center gap-2 text-xs text-zinc-500">
          <StatusPill status={app.status} />
          {app.pid > 0 && app.status === "running" && <span>pid {app.pid}</span>}
          {(app.ports ?? []).map((p) => (
            <span key={`${p.proto}${p.port}`} className="rounded bg-zinc-800 px-1.5 py-0.5">
              :{p.port}
            </span>
          ))}
        </div>
        {app.approval !== "approved" && (
          <p className="mt-1 text-xs text-amber-500">
            awaiting host approval — run{" "}
            <code className="rounded bg-zinc-800 px-1">breakerbox-agent apps approve {app.id}</code>
          </p>
        )}
      </div>
      <Toggle
        on={running}
        busy={busy}
        disabled={app.approval !== "approved"}
        title={app.approval !== "approved" ? "Approve on the host first" : running ? "Stop" : "Start"}
        onChange={toggle}
      />
    </li>
  );
}

function EnrollModal({ onClose }: { onClose: () => void }) {
  const [token, setToken] = useState<string>();
  const [error, setError] = useState("");

  useEffect(() => {
    fetch("/api/bb/enroll-tokens", {
      method: "POST",
      headers: { Authorization: pb.authStore.token },
    })
      .then((r) => (r.ok ? r.json() : Promise.reject(r.status)))
      .then((d) => setToken(d.token))
      .catch(() => setError("Could not mint an enrollment token."));
  }, []);

  const oneLiner = token
    ? `breakerbox-agent enroll --hub ${window.location.origin} --token ${token}\nbreakerbox-agent run`
    : "…";

  return (
    <Modal title="Add a system" onClose={onClose}>
      <p className="mb-3 text-sm text-zinc-400">
        On the machine you want to control, install the agent, then run (token valid 30 minutes, single use):
      </p>
      {error ? <p className="text-sm text-red-400">{error}</p> : <CopyBlock text={oneLiner} />}
      <p className="mt-3 text-xs text-zinc-500">
        The agent dials out to this hub — no ports to open on the machine.
      </p>
    </Modal>
  );
}

function AddAppModal({ system, onClose }: { system: SystemRecord; onClose: () => void }) {
  const [tab, setTab] = useState<"ai" | "paste">("ai");
  const [json, setJson] = useState("");
  const [msg, setMsg] = useState<{ ok: boolean; text: string }>();
  const prompt = generateAppDefPrompt(system.os || "macOS/Linux");

  async function importJson() {
    setMsg(undefined);
    let def: unknown;
    try {
      def = JSON.parse(json);
    } catch {
      setMsg({ ok: false, text: "That's not valid JSON." });
      return;
    }
    const r = await fetch("/api/bb/apps", {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: pb.authStore.token },
      body: JSON.stringify({ system: system.id, definition: def }),
    });
    const body = await r.json().catch(() => ({}));
    if (r.ok) {
      setMsg({
        ok: true,
        text: `Registered. Approve it on the host: breakerbox-agent apps approve ${body.app_id}`,
      });
    } else {
      setMsg({ ok: false, text: body.message || "Import failed." });
    }
  }

  return (
    <Modal title={`Add app on ${system.name}`} onClose={onClose} wide>
      <div className="mb-4 flex gap-2">
        {(
          [
            ["ai", "🤖 Generate with AI"],
            ["paste", "📋 Paste breakerbox.app.json"],
          ] as const
        ).map(([key, label]) => (
          <button
            key={key}
            onClick={() => setTab(key)}
            className={`rounded-lg px-3 py-1.5 text-sm ${tab === key ? "bg-amber-500 font-semibold text-zinc-950" : "bg-zinc-800 text-zinc-300 hover:bg-zinc-700"}`}
          >
            {label}
          </button>
        ))}
      </div>

      {tab === "ai" ? (
        <div className="space-y-3 text-sm">
          <ol className="list-decimal space-y-1 pl-5 text-zinc-300">
            <li>Copy the prompt below.</li>
            <li>
              Run <code className="rounded bg-zinc-800 px-1">claude</code> (or{" "}
              <code className="rounded bg-zinc-800 px-1">codex</code>) inside your app's project folder and paste it.
            </li>
            <li>
              It writes <code className="rounded bg-zinc-800 px-1">breakerbox.app.json</code>; import it on that machine
              with{" "}
              <code className="rounded bg-zinc-800 px-1">breakerbox-agent apps import breakerbox.app.json</code>{" "}
              (registers <em>and</em> approves), or paste the JSON in the other tab.
            </li>
          </ol>
          <CopyBlock text={prompt} />
        </div>
      ) : (
        <div className="space-y-3">
          <textarea
            value={json}
            onChange={(e) => setJson(e.target.value)}
            placeholder='{"schema_version":1,"name":"my-app","cmd":"npm","args":["run","start"],...}'
            rows={10}
            className="w-full rounded-lg border border-zinc-800 bg-zinc-950 p-3 font-mono text-xs focus:border-amber-500 focus:outline-none"
          />
          <div className="flex items-center justify-between">
            <p className="text-xs text-zinc-500">Pasted definitions still require approval on the host.</p>
            <button
              onClick={importJson}
              className="rounded-lg bg-amber-500 px-4 py-1.5 text-sm font-semibold text-zinc-950 hover:bg-amber-400"
            >
              Register app
            </button>
          </div>
          {msg && <p className={`text-sm ${msg.ok ? "text-emerald-400" : "text-red-400"}`}>{msg.text}</p>}
        </div>
      )}
    </Modal>
  );
}
