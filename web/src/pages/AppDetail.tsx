import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { pb, sendCommand } from "../lib/pb";
import type { AppRecord } from "../lib/pb";
import { StatusPill, Toggle } from "../components/ui";
import {
  ChartCard,
  LineChart,
  RangePicker,
  fmtBytes,
  seriesOf,
  useMetricHistory,
  type RangeKey,
} from "../components/charts";

export default function AppDetail() {
  const { id } = useParams<{ id: string }>();
  const nav = useNavigate();
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!pb.authStore.isValid) nav("/login");
  }, [nav]);

  const [range, setRange] = useState<RangeKey>("1h");
  const { data: app } = useQuery({
    queryKey: ["app", id],
    queryFn: () => pb.collection("apps").getOne<AppRecord>(id!),
    enabled: !!id,
  });
  const { data: metrics } = useMetricHistory("app_metrics", "app", id, range);

  useEffect(() => {
    if (!id) return;
    pb.collection("apps")
      .subscribe(id, () => qc.invalidateQueries({ queryKey: ["app", id] }))
      .catch(() => {});
    return () => {
      pb.collection("apps").unsubscribe(id).catch(() => {});
    };
  }, [id, qc]);

  if (!app) return <div className="p-8 text-zinc-500">Loading…</div>;

  const running = app.status === "running" || app.status === "starting";
  const cpu = seriesOf(metrics, "cpu");
  const mem = seriesOf(metrics, "mem_rss");

  async function act(verb: "start" | "stop" | "restart") {
    setBusy(true);
    try {
      await sendCommand(app!, verb);
    } finally {
      setTimeout(() => setBusy(false), 800);
    }
  }

  return (
    <div className="mx-auto max-w-3xl px-4 py-8">
      <Link to="/" className="text-sm text-zinc-400 hover:text-zinc-200">
        ← All systems
      </Link>

      <div className="mt-4 flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold">{app.name}</h1>
          <div className="mt-1 flex items-center gap-2 text-sm text-zinc-400">
            <StatusPill status={app.status} />
            {app.pid > 0 && running && <span>pid {app.pid}</span>}
            {(app.ports ?? []).map((p) => (
              <span key={`${p.proto}${p.port}`} className="rounded bg-zinc-800 px-1.5 py-0.5 text-xs">
                {p.proto}:{p.port}
              </span>
            ))}
          </div>
        </div>
        <div className="flex items-center gap-3">
          <button
            onClick={() => act("restart")}
            disabled={busy || app.approval !== "approved"}
            className="rounded-lg bg-zinc-800 px-3 py-1.5 text-sm hover:bg-zinc-700 disabled:opacity-40"
          >
            ⟳ Restart
          </button>
          <Toggle on={running} busy={busy} disabled={app.approval !== "approved"} onChange={(n) => act(n ? "start" : "stop")} />
        </div>
      </div>

      {app.approval !== "approved" && (
        <div className="mt-4 rounded-lg border border-amber-500/30 bg-amber-500/10 p-3 text-sm text-amber-400">
          This definition awaits approval on its host. Run:{" "}
          <code className="rounded bg-zinc-900 px-1.5 py-0.5">breakerbox-agent apps approve {app.id}</code>
        </div>
      )}

      <section className="mt-8">
        <div className="mb-3 flex justify-end">
          <RangePicker value={range} onChange={setRange} />
        </div>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <ChartCard title="CPU" latest={cpu.at(-1) ? cpu.at(-1)!.v.toFixed(1) : "—"} unit="%">
            <LineChart points={cpu} unit="%" />
          </ChartCard>
          <ChartCard title="Memory" latest={mem.at(-1) ? fmtBytes(mem.at(-1)!.v) : "—"} unit="B">
            <LineChart points={mem} unit="B" fmt={fmtBytes} color="rgb(96 165 250)" />
          </ChartCard>
        </div>
      </section>

      <section className="mt-8">
        <h2 className="mb-2 text-sm font-semibold uppercase tracking-wide text-zinc-400">Logs</h2>
        <LogViewer appId={app.id} />
      </section>

      <section className="mt-8">
        <h2 className="mb-2 text-sm font-semibold uppercase tracking-wide text-zinc-400">Definition</h2>
        <pre className="overflow-x-auto rounded-lg border border-zinc-800 bg-zinc-900/60 p-4 text-xs text-zinc-300">
          {JSON.stringify(app.definition, null, 2)}
        </pre>
        <p className="mt-2 text-xs text-zinc-600">hash {app.definition_hash}</p>
      </section>
    </div>
  );
}

const MAX_LOG_LINES = 2000;

function LogViewer({ appId }: { appId: string }) {
  const [lines, setLines] = useState<string[]>([]);
  const [state, setState] = useState<"connecting" | "live" | "ended" | "error">("connecting");
  const [paused, setPaused] = useState(false);
  const [box, setBox] = useState<HTMLDivElement | null>(null);

  useEffect(() => {
    setLines([]);
    setState("connecting");
    const url = `/api/bb/apps/${appId}/logs?tail=200&token=${encodeURIComponent(pb.authStore.token)}`;
    const es = new EventSource(url);
    es.onopen = () => setState("live");
    es.onmessage = (ev) => {
      try {
        const batch = JSON.parse(ev.data) as string[];
        setLines((prev) => {
          const next = prev.concat(batch);
          return next.length > MAX_LOG_LINES ? next.slice(next.length - MAX_LOG_LINES) : next;
        });
      } catch {
        /* ignore malformed frames */
      }
    };
    es.addEventListener("eof", () => {
      setState("ended");
      es.close();
    });
    es.onerror = () => {
      // EventSource retries transient errors itself; a closed stream after
      // eof lands here too, so only flag real failures while connecting.
      setState((s) => (s === "connecting" ? "error" : s));
    };
    return () => es.close();
  }, [appId]);

  useEffect(() => {
    if (box && !paused) box.scrollTop = box.scrollHeight;
  }, [lines, box, paused]);

  return (
    <div className="rounded-lg border border-zinc-800 bg-zinc-950">
      <div className="flex items-center justify-between border-b border-zinc-800 px-3 py-1.5 text-xs text-zinc-500">
        <span>
          {state === "live" && <span className="text-emerald-500">● streaming</span>}
          {state === "connecting" && "connecting…"}
          {state === "ended" && "stream ended"}
          {state === "error" && <span className="text-red-400">agent offline or stream unavailable</span>}
        </span>
        <button onClick={() => setPaused((p) => !p)} className="rounded px-2 py-0.5 hover:bg-zinc-800">
          {paused ? "▶ resume scroll" : "⏸ pause scroll"}
        </button>
      </div>
      <div ref={setBox} className="h-72 overflow-y-auto p-3 font-mono text-xs leading-5 text-zinc-300">
        {lines.length === 0 ? (
          <span className="text-zinc-600">no output yet</span>
        ) : (
          lines.map((l, i) => <div key={i} className="whitespace-pre-wrap break-all">{l}</div>)
        )}
      </div>
    </div>
  );
}

