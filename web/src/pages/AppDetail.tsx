import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { pb, sendCommand } from "../lib/pb";
import type { AppMetricRecord, AppRecord } from "../lib/pb";
import { StatusPill, Toggle } from "../components/ui";

export default function AppDetail() {
  const { id } = useParams<{ id: string }>();
  const nav = useNavigate();
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!pb.authStore.isValid) nav("/login");
  }, [nav]);

  const { data: app } = useQuery({
    queryKey: ["app", id],
    queryFn: () => pb.collection("apps").getOne<AppRecord>(id!),
    enabled: !!id,
  });
  const { data: metrics } = useQuery({
    queryKey: ["app_metrics", id],
    queryFn: () =>
      pb.collection("app_metrics").getList<AppMetricRecord>(1, 60, {
        filter: `app = "${id}" && type = "1m"`,
        sort: "-created",
      }),
    enabled: !!id,
    refetchInterval: 15000,
  });

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
  const samples = [...(metrics?.items ?? [])].reverse();

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

      <section className="mt-8 grid grid-cols-2 gap-4">
        <MetricCard
          title="CPU"
          unit="%"
          values={samples.map((s) => s.cpu)}
          latest={samples.at(-1)?.cpu}
          fmt={(v) => v.toFixed(1)}
        />
        <MetricCard
          title="Memory"
          unit="MB"
          values={samples.map((s) => s.mem_rss / 1048576)}
          latest={(samples.at(-1)?.mem_rss ?? 0) / 1048576}
          fmt={(v) => v.toFixed(0)}
        />
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

function MetricCard({
  title,
  unit,
  values,
  latest,
  fmt,
}: {
  title: string;
  unit: string;
  values: number[];
  latest?: number;
  fmt: (v: number) => string;
}) {
  return (
    <div className="rounded-xl border border-zinc-800 bg-zinc-900/50 p-4">
      <div className="flex items-baseline justify-between">
        <span className="text-sm text-zinc-400">{title}</span>
        <span className="text-lg font-semibold">
          {latest !== undefined && values.length > 0 ? fmt(latest) : "—"}
          <span className="ml-1 text-xs text-zinc-500">{unit}</span>
        </span>
      </div>
      <Sparkline values={values} />
    </div>
  );
}

function Sparkline({ values }: { values: number[] }) {
  if (values.length < 2)
    return <div className="mt-3 h-12 text-center text-xs leading-[3rem] text-zinc-600">collecting…</div>;
  const w = 240;
  const h = 48;
  const max = Math.max(...values, 0.001);
  const pts = values
    .map((v, i) => `${((i / (values.length - 1)) * w).toFixed(1)},${(h - (v / max) * (h - 4) - 2).toFixed(1)}`)
    .join(" ");
  return (
    <svg viewBox={`0 0 ${w} ${h}`} className="mt-3 h-12 w-full">
      <polyline points={pts} fill="none" stroke="rgb(245 158 11)" strokeWidth="1.5" />
    </svg>
  );
}
