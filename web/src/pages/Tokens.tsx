import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { pb } from "../lib/pb";

interface Summary {
  days: number;
  day_model: { day: string; model: string; cost: number }[];
  by_app: { app_id: string; name: string; cost: number; input: number; output: number; rows: number }[];
  by_source: { key: string; cost: number }[];
  by_model: { key: string; cost: number }[];
  totals: { cost: number; input: number; output: number; rows: number };
}

const DAY_OPTIONS = [7, 30, 90] as const;

const MODEL_COLORS = [
  "rgb(245 158 11)",
  "rgb(96 165 250)",
  "rgb(52 211 153)",
  "rgb(192 132 252)",
  "rgb(244 114 182)",
  "rgb(148 163 184)",
];

const SOURCE_LABELS: Record<string, string> = {
  claude_code: "Claude Code",
  codex: "Codex CLI",
  runtime_proxy: "Runtime proxy",
};

function usd(v: number): string {
  return v >= 100 ? `$${v.toFixed(0)}` : v >= 1 ? `$${v.toFixed(2)}` : `$${v.toFixed(3)}`;
}

function tokens(v: number): string {
  if (v >= 1e9) return `${(v / 1e9).toFixed(1)}B`;
  if (v >= 1e6) return `${(v / 1e6).toFixed(1)}M`;
  if (v >= 1e3) return `${(v / 1e3).toFixed(0)}k`;
  return `${v}`;
}

export default function Tokens() {
  const nav = useNavigate();
  const [days, setDays] = useState<(typeof DAY_OPTIONS)[number]>(30);

  useEffect(() => {
    if (!pb.authStore.isValid) nav("/login");
  }, [nav]);

  const { data, isLoading } = useQuery({
    queryKey: ["token-summary", days],
    queryFn: () =>
      pb.send<Summary>(`/api/bb/tokens/summary?days=${days}`, { method: "GET" }),
    refetchInterval: 30_000,
  });

  return (
    <div className="mx-auto max-w-5xl px-4 py-8">
      <Link to="/" className="text-sm text-zinc-400 hover:text-zinc-200">
        ← All systems
      </Link>
      <div className="mt-4 flex items-center justify-between">
        <h1 className="text-2xl font-bold">Token usage</h1>
        <div className="flex gap-1 rounded-lg border border-zinc-800 p-0.5 text-xs">
          {DAY_OPTIONS.map((d) => (
            <button
              key={d}
              onClick={() => setDays(d)}
              className={`rounded-md px-2 py-1 ${days === d ? "bg-zinc-700 text-zinc-100" : "text-zinc-400 hover:text-zinc-200"}`}
            >
              {d}d
            </button>
          ))}
        </div>
      </div>

      {isLoading || !data ? (
        <div className="mt-10 text-zinc-500">Loading…</div>
      ) : data.totals.rows === 0 ? (
        <div className="mt-10 rounded-xl border border-dashed border-zinc-800 p-10 text-center text-zinc-400">
          <p className="font-medium">No token usage recorded yet.</p>
          <p className="mt-2 text-sm">
            Usage appears automatically when Claude Code or Codex runs on an enrolled machine —
            no configuration needed.
          </p>
        </div>
      ) : (
        <>
          <section className="mt-6 grid grid-cols-2 gap-4 sm:grid-cols-4">
            <Stat label={`Spend (${days}d)`} value={usd(data.totals.cost)} />
            <Stat label="Input tokens" value={tokens(data.totals.input)} />
            <Stat label="Output tokens" value={tokens(data.totals.output)} />
            <Stat label="API calls" value={tokens(data.totals.rows)} />
          </section>

          <section className="mt-8">
            <h2 className="mb-2 text-sm font-semibold uppercase tracking-wide text-zinc-400">
              Daily spend by model
            </h2>
            <DailyStack data={data} />
          </section>

          <div className="mt-8 grid grid-cols-1 gap-8 md:grid-cols-2">
            <section>
              <h2 className="mb-2 text-sm font-semibold uppercase tracking-wide text-zinc-400">By app</h2>
              <table className="w-full text-sm">
                <tbody>
                  {data.by_app.map((a) => (
                    <tr key={a.app_id || "system"} className="border-t border-zinc-800/60">
                      <td className="py-2">
                        {a.app_id ? (
                          <Link to={`/apps/${a.app_id}`} className="hover:text-amber-400">
                            {a.name || a.app_id}
                          </Link>
                        ) : (
                          <span className="text-zinc-500" title="Sessions in directories not registered as apps">
                            Unattributed
                          </span>
                        )}
                      </td>
                      <td className="py-2 text-right text-zinc-400">
                        {tokens(a.input + a.output)} tok
                      </td>
                      <td className="py-2 text-right font-medium">{usd(a.cost)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </section>
            <section>
              <h2 className="mb-2 text-sm font-semibold uppercase tracking-wide text-zinc-400">
                By model & source
              </h2>
              <table className="w-full text-sm">
                <tbody>
                  {data.by_model.map((m, i) => (
                    <tr key={m.key} className="border-t border-zinc-800/60">
                      <td className="py-2">
                        <span
                          className="mr-2 inline-block h-2 w-2 rounded-full"
                          style={{ background: MODEL_COLORS[i % MODEL_COLORS.length] }}
                        />
                        {m.key || "unknown"}
                      </td>
                      <td className="py-2 text-right font-medium">{usd(m.cost)}</td>
                    </tr>
                  ))}
                  {data.by_source.map((s) => (
                    <tr key={s.key} className="border-t border-zinc-800/60 text-zinc-400">
                      <td className="py-2">{SOURCE_LABELS[s.key] ?? s.key}</td>
                      <td className="py-2 text-right">{usd(s.cost)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </section>
          </div>
        </>
      )}
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-xl border border-zinc-800 bg-zinc-900/50 p-4">
      <div className="text-xs text-zinc-500">{label}</div>
      <div className="mt-1 text-xl font-bold">{value}</div>
    </div>
  );
}

function DailyStack({ data }: { data: Summary }) {
  // Pivot day_model rows into per-day stacks with a stable model order.
  const models = data.by_model.map((m) => m.key);
  const dayMap = new Map<string, Map<string, number>>();
  for (const r of data.day_model) {
    if (!dayMap.has(r.day)) dayMap.set(r.day, new Map());
    dayMap.get(r.day)!.set(r.model, (dayMap.get(r.day)!.get(r.model) ?? 0) + r.cost);
  }
  const daysList: string[] = [];
  for (let i = data.days - 1; i >= 0; i--) {
    daysList.push(new Date(Date.now() - i * 86_400_000).toISOString().slice(0, 10));
  }
  const totals = daysList.map((d) => {
    let sum = 0;
    dayMap.get(d)?.forEach((v) => (sum += v));
    return sum;
  });
  const maxDay = Math.max(...totals, 0.0001);

  const w = 720;
  const h = 160;
  const gap = 2;
  const bw = Math.max((w - gap * daysList.length) / daysList.length, 2);
  return (
    <div className="rounded-xl border border-zinc-800 bg-zinc-900/50 p-4">
      <svg viewBox={`0 0 ${w} ${h}`} className="w-full" style={{ height: h }}>
        {daysList.map((d, i) => {
          let y = h - 18;
          const stack = dayMap.get(d);
          return (
            <g key={d}>
              {models.map((m, mi) => {
                const v = stack?.get(m) ?? 0;
                if (v <= 0) return null;
                const bh = (v / maxDay) * (h - 34);
                y -= bh;
                return (
                  <rect
                    key={m}
                    x={i * (bw + gap)}
                    y={y}
                    width={bw}
                    height={bh}
                    fill={MODEL_COLORS[mi % MODEL_COLORS.length]}
                  >
                    <title>{`${d} · ${m}: ${usd(v)}`}</title>
                  </rect>
                );
              })}
            </g>
          );
        })}
        <text x="0" y={h - 4} fontSize="9" className="fill-zinc-600">
          {daysList[0]}
        </text>
        <text x={w} y={h - 4} fontSize="9" textAnchor="end" className="fill-zinc-600">
          {daysList.at(-1)}
        </text>
        <text x="0" y="10" fontSize="9" className="fill-zinc-500">
          max {usd(maxDay)}/day
        </text>
      </svg>
    </div>
  );
}
