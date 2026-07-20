import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { pb } from "../lib/pb";

// Range -> tier mapping mirrors the hub's retention tiers: each range uses
// the finest tier guaranteed to still hold data for the whole window.
export const RANGES = [
  { key: "1h", label: "1h", tier: "1m", ms: 3_600_000 },
  { key: "24h", label: "24h", tier: "10m", ms: 86_400_000 },
  { key: "7d", label: "7d", tier: "1h", ms: 7 * 86_400_000 },
  { key: "30d", label: "30d", tier: "1h", ms: 30 * 86_400_000 },
] as const;
export type RangeKey = (typeof RANGES)[number]["key"];

interface MetricRow {
  created: string;
  [field: string]: unknown;
}

export function useMetricHistory(
  collection: "system_metrics" | "app_metrics",
  keyField: "system" | "app",
  id: string | undefined,
  range: RangeKey,
) {
  const r = RANGES.find((x) => x.key === range)!;
  return useQuery({
    queryKey: ["history", collection, id, range],
    queryFn: async () => {
      const cutoff = new Date(Date.now() - r.ms).toISOString().replace("T", " ");
      const rows = await pb.collection(collection).getFullList<MetricRow>({
        filter: `${keyField} = "${id}" && type = "${r.tier}" && created >= "${cutoff}"`,
        sort: "created",
        batch: 1000,
      });
      return rows;
    },
    enabled: !!id,
    refetchInterval: 30_000,
  });
}

export function RangePicker({ value, onChange }: { value: RangeKey; onChange: (r: RangeKey) => void }) {
  return (
    <div className="flex gap-1 rounded-lg border border-zinc-800 p-0.5 text-xs">
      {RANGES.map((r) => (
        <button
          key={r.key}
          onClick={() => onChange(r.key)}
          className={`rounded-md px-2 py-1 ${
            value === r.key ? "bg-zinc-700 text-zinc-100" : "text-zinc-400 hover:text-zinc-200"
          }`}
        >
          {r.label}
        </button>
      ))}
    </div>
  );
}

/** numeric series out of raw rows; counters become per-second rates. */
export function seriesOf(
  rows: MetricRow[] | undefined,
  field: string,
  opts?: { counterRate?: boolean; scale?: number },
): { t: number; v: number }[] {
  if (!rows) return [];
  const scale = opts?.scale ?? 1;
  const pts = rows.map((r) => ({
    t: new Date(r.created.replace(" ", "T")).getTime(),
    v: Number(r[field] ?? 0) * scale,
  }));
  if (!opts?.counterRate) return pts;
  const out: { t: number; v: number }[] = [];
  for (let i = 1; i < pts.length; i++) {
    const dt = (pts[i].t - pts[i - 1].t) / 1000;
    const dv = pts[i].v - pts[i - 1].v;
    // Counter reset (reboot) shows as a negative delta: clamp to zero.
    out.push({ t: pts[i].t, v: dt > 0 && dv >= 0 ? dv / dt : 0 });
  }
  return out;
}

export function LineChart({
  points,
  unit,
  fmt = (v) => v.toFixed(1),
  height = 120,
  color = "rgb(245 158 11)",
}: {
  points: { t: number; v: number }[];
  unit: string;
  fmt?: (v: number) => string;
  height?: number;
  color?: string;
}) {
  const w = 560;
  const h = height;
  if (points.length < 2) {
    return (
      <div className="flex items-center justify-center text-xs text-zinc-600" style={{ height: h }}>
        not enough data yet
      </div>
    );
  }
  const t0 = points[0].t;
  const t1 = points[points.length - 1].t;
  const vMax = Math.max(...points.map((p) => p.v), 0.001);
  const x = (t: number) => ((t - t0) / Math.max(t1 - t0, 1)) * (w - 8) + 4;
  const y = (v: number) => h - 14 - (v / vMax) * (h - 26);
  const path = points.map((p, i) => `${i === 0 ? "M" : "L"}${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`).join(" ");
  const area = `${path} L${x(t1).toFixed(1)},${h - 14} L${x(t0).toFixed(1)},${h - 14} Z`;
  const timeLabel = (t: number) => {
    const d = new Date(t);
    return t1 - t0 > 36e5 * 26
      ? d.toLocaleDateString(undefined, { month: "short", day: "numeric" })
      : d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  };
  return (
    <svg viewBox={`0 0 ${w} ${h}`} className="w-full" style={{ height: h }}>
      <path d={area} fill={color} opacity="0.08" />
      <path d={path} fill="none" stroke={color} strokeWidth="1.5" />
      <text x="4" y="10" className="fill-zinc-500" fontSize="9">
        {fmt(vMax)} {unit}
      </text>
      <text x="4" y={h - 3} className="fill-zinc-600" fontSize="9">
        {timeLabel(t0)}
      </text>
      <text x={w - 4} y={h - 3} textAnchor="end" className="fill-zinc-600" fontSize="9">
        {timeLabel(t1)}
      </text>
    </svg>
  );
}

export function ChartCard({
  title,
  latest,
  unit,
  children,
}: {
  title: string;
  latest?: string;
  unit?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="rounded-xl border border-zinc-800 bg-zinc-900/50 p-4">
      <div className="mb-2 flex items-baseline justify-between">
        <span className="text-sm text-zinc-400">{title}</span>
        {latest !== undefined && (
          <span className="text-lg font-semibold">
            {latest}
            {unit && <span className="ml-1 text-xs text-zinc-500">{unit}</span>}
          </span>
        )}
      </div>
      {children}
    </div>
  );
}

const KB = 1024;
export function fmtBytes(v: number): string {
  if (v >= KB * KB * KB) return `${(v / KB / KB / KB).toFixed(1)}G`;
  if (v >= KB * KB) return `${(v / KB / KB).toFixed(1)}M`;
  if (v >= KB) return `${(v / KB).toFixed(0)}K`;
  return `${v.toFixed(0)}`;
}

/** Ready-made host history panel (used by the system history modal). */
export function SystemHistory({ systemId }: { systemId: string }) {
  const [range, setRange] = useState<RangeKey>("1h");
  const { data } = useMetricHistory("system_metrics", "system", systemId, range);
  const cpu = seriesOf(data, "cpu");
  const mem = seriesOf(data, "mem_pct");
  const disk = seriesOf(data, "disk_pct");
  const netUp = seriesOf(data, "net_sent", { counterRate: true });
  const netDown = seriesOf(data, "net_recv", { counterRate: true });
  return (
    <div>
      <div className="mb-3 flex justify-end">
        <RangePicker value={range} onChange={setRange} />
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <ChartCard title="CPU" latest={cpu.at(-1) ? cpu.at(-1)!.v.toFixed(1) : "—"} unit="%">
          <LineChart points={cpu} unit="%" />
        </ChartCard>
        <ChartCard title="Memory" latest={mem.at(-1) ? mem.at(-1)!.v.toFixed(1) : "—"} unit="%">
          <LineChart points={mem} unit="%" color="rgb(96 165 250)" />
        </ChartCard>
        <ChartCard title="Disk" latest={disk.at(-1) ? disk.at(-1)!.v.toFixed(1) : "—"} unit="%">
          <LineChart points={disk} unit="%" color="rgb(52 211 153)" />
        </ChartCard>
        <ChartCard
          title="Network ↑ / ↓"
          latest={netUp.at(-1) ? `${fmtBytes(netUp.at(-1)!.v)}/s ${fmtBytes(netDown.at(-1)?.v ?? 0)}/s` : "—"}
        >
          <LineChart points={netUp} unit="B/s" fmt={fmtBytes} color="rgb(192 132 252)" height={56} />
          <LineChart points={netDown} unit="B/s" fmt={fmtBytes} color="rgb(244 114 182)" height={56} />
        </ChartCard>
      </div>
    </div>
  );
}
