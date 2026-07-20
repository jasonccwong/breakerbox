import { useState } from "react";
import { Pressable, ScrollView, StyleSheet, Text, View } from "react-native";
import { useQuery } from "@tanstack/react-query";
import { pb } from "../lib/pb";
import { colors } from "../lib/theme";

interface Summary {
  by_app: { app_id: string; name: string; cost: number; input: number; output: number }[];
  by_model: { key: string; cost: number }[];
  by_source: { key: string; cost: number }[];
  totals: { cost: number; input: number; output: number; rows: number };
}

const DAY_OPTIONS = [7, 30, 90] as const;

const SOURCE_LABELS: Record<string, string> = {
  claude_code: "Claude Code",
  codex: "Codex CLI",
  runtime_proxy: "Runtime proxy",
};

function usd(v: number): string {
  return v >= 100 ? `$${v.toFixed(0)}` : v >= 1 ? `$${v.toFixed(2)}` : `$${v.toFixed(3)}`;
}
function tok(v: number): string {
  if (v >= 1e9) return `${(v / 1e9).toFixed(1)}B`;
  if (v >= 1e6) return `${(v / 1e6).toFixed(1)}M`;
  if (v >= 1e3) return `${(v / 1e3).toFixed(0)}k`;
  return `${v}`;
}

export default function Tokens() {
  const [days, setDays] = useState<(typeof DAY_OPTIONS)[number]>(30);
  const { data } = useQuery({
    queryKey: ["token-summary", days],
    queryFn: () => pb()!.send<Summary>(`/api/bb/tokens/summary?days=${days}`, { method: "GET" }),
    enabled: !!pb(),
    refetchInterval: 30_000,
  });

  return (
    <ScrollView style={styles.wrap}>
      <View style={styles.rangeRow}>
        {DAY_OPTIONS.map((d) => (
          <Pressable
            key={d}
            style={[styles.rangeBtn, days === d && styles.rangeBtnActive]}
            onPress={() => setDays(d)}
          >
            <Text style={[styles.rangeText, days === d && styles.rangeTextActive]}>{d}d</Text>
          </Pressable>
        ))}
      </View>

      {!data ? (
        <Text style={styles.dim}>Loading…</Text>
      ) : data.totals.rows === 0 ? (
        <Text style={styles.empty}>
          No token usage yet. It appears automatically when Claude Code or Codex runs on an
          enrolled machine.
        </Text>
      ) : (
        <>
          <View style={styles.statRow}>
            <Stat label={`Spend (${days}d)`} value={usd(data.totals.cost)} />
            <Stat label="Calls" value={tok(data.totals.rows)} />
          </View>
          <View style={styles.statRow}>
            <Stat label="Input tokens" value={tok(data.totals.input)} />
            <Stat label="Output tokens" value={tok(data.totals.output)} />
          </View>

          <Text style={styles.section}>By app</Text>
          <View style={styles.card}>
            {data.by_app.map((a) => (
              <View key={a.app_id || "sys"} style={styles.row}>
                <Text style={[styles.rowName, !a.app_id && { color: colors.faint }]}>
                  {a.app_id ? a.name || a.app_id : "Unattributed"}
                </Text>
                <Text style={styles.rowTokens}>{tok(a.input + a.output)}</Text>
                <Text style={styles.rowCost}>{usd(a.cost)}</Text>
              </View>
            ))}
          </View>

          <Text style={styles.section}>By model</Text>
          <View style={styles.card}>
            {data.by_model.map((m) => (
              <View key={m.key} style={styles.row}>
                <Text style={styles.rowName}>{m.key || "unknown"}</Text>
                <Text style={styles.rowCost}>{usd(m.cost)}</Text>
              </View>
            ))}
            {data.by_source.map((s) => (
              <View key={s.key} style={styles.row}>
                <Text style={[styles.rowName, { color: colors.faint }]}>
                  {SOURCE_LABELS[s.key] ?? s.key}
                </Text>
                <Text style={[styles.rowCost, { color: colors.dim }]}>{usd(s.cost)}</Text>
              </View>
            ))}
          </View>
        </>
      )}
      <View style={{ height: 32 }} />
    </ScrollView>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <View style={styles.statCard}>
      <Text style={styles.statLabel}>{label}</Text>
      <Text style={styles.statValue}>{value}</Text>
    </View>
  );
}

const styles = StyleSheet.create({
  wrap: { flex: 1, backgroundColor: colors.bg, padding: 16 },
  dim: { color: colors.faint },
  empty: { color: colors.dim, textAlign: "center", marginTop: 40, lineHeight: 20 },
  rangeRow: { flexDirection: "row", gap: 8, marginBottom: 14 },
  rangeBtn: {
    borderColor: colors.border,
    borderWidth: 1,
    borderRadius: 8,
    paddingVertical: 6,
    paddingHorizontal: 14,
  },
  rangeBtnActive: { backgroundColor: colors.accent, borderColor: colors.accent },
  rangeText: { color: colors.dim, fontSize: 13 },
  rangeTextActive: { color: colors.accentText, fontWeight: "700" },
  statRow: { flexDirection: "row", gap: 12, marginBottom: 12 },
  statCard: {
    flex: 1,
    backgroundColor: colors.card,
    borderColor: colors.border,
    borderWidth: 1,
    borderRadius: 12,
    padding: 14,
  },
  statLabel: { color: colors.dim, fontSize: 12 },
  statValue: { color: colors.text, fontSize: 20, fontWeight: "700", marginTop: 4 },
  section: {
    color: colors.faint,
    fontSize: 12,
    textTransform: "uppercase",
    marginTop: 14,
    marginBottom: 6,
  },
  card: {
    backgroundColor: colors.card,
    borderColor: colors.border,
    borderWidth: 1,
    borderRadius: 12,
    paddingHorizontal: 14,
  },
  row: {
    flexDirection: "row",
    alignItems: "center",
    paddingVertical: 10,
    borderTopColor: colors.border,
    borderTopWidth: StyleSheet.hairlineWidth,
  },
  rowName: { color: colors.text, fontSize: 14, flex: 1 },
  rowTokens: { color: colors.faint, fontSize: 13, marginRight: 12 },
  rowCost: { color: colors.text, fontSize: 14, fontWeight: "600" },
});
