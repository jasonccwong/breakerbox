import { useEffect, useState } from "react";
import {
  Pressable,
  ScrollView,
  StyleSheet,
  Switch,
  Text,
  View,
} from "react-native";
import { Stack, useLocalSearchParams } from "expo-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { hubUrl, pb } from "../../lib/pb";
import { colors, statusColor } from "../../lib/theme";

interface AppRec {
  id: string;
  system: string;
  name: string;
  status: string;
  approval: string;
  pid: number;
  ports: { proto: string; port: number }[] | null;
}
interface MetricRec {
  cpu: number;
  mem_rss: number;
  created: string;
}

export default function AppDetail() {
  const { id } = useLocalSearchParams<{ id: string }>();
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);

  const { data: app } = useQuery({
    queryKey: ["app", id],
    queryFn: () => pb()!.collection("apps").getOne<AppRec>(id!),
    enabled: !!pb() && !!id,
    refetchInterval: 4_000,
  });
  const { data: metrics } = useQuery({
    queryKey: ["app-metrics", id],
    queryFn: () =>
      pb()!
        .collection("app_metrics")
        .getList<MetricRec>(1, 1, { filter: `app = "${id}" && type = "1m"`, sort: "-created" }),
    enabled: !!pb() && !!id,
    refetchInterval: 15_000,
  });

  async function act(verb: "start" | "stop" | "restart") {
    if (!app) return;
    setBusy(true);
    try {
      await pb()!.collection("commands").create({
        app: app.id,
        system: app.system,
        verb,
        status: "pending",
      });
    } finally {
      setTimeout(() => {
        setBusy(false);
        qc.invalidateQueries({ queryKey: ["app", id] });
      }, 1200);
    }
  }

  if (!app) {
    return (
      <View style={styles.wrap}>
        <Text style={styles.dim}>Loading…</Text>
      </View>
    );
  }

  const running = app.status === "running" || app.status === "starting";
  const latest = metrics?.items?.[0];

  return (
    <ScrollView style={styles.wrap}>
      <Stack.Screen options={{ title: app.name }} />
      <View style={styles.headerRow}>
        <View style={[styles.dot, { backgroundColor: statusColor[app.status] ?? colors.faint }]} />
        <Text style={styles.status}>{app.status}</Text>
        {app.pid > 0 && running && <Text style={styles.pid}>pid {app.pid}</Text>}
        <View style={{ flex: 1 }} />
        <Switch
          value={running}
          disabled={busy || app.approval !== "approved"}
          onValueChange={(v) => act(v ? "start" : "stop")}
          trackColor={{ true: colors.accent, false: colors.border }}
          thumbColor="#fafafa"
        />
      </View>

      {app.approval !== "approved" && (
        <View style={styles.warn}>
          <Text style={styles.warnText}>
            Awaiting approval on its host — run: breakerbox-agent apps approve {app.id}
          </Text>
        </View>
      )}

      <View style={styles.statRow}>
        <View style={styles.statCard}>
          <Text style={styles.statLabel}>CPU</Text>
          <Text style={styles.statValue}>
            {latest ? latest.cpu.toFixed(1) : "—"}
            <Text style={styles.statUnit}> %</Text>
          </Text>
        </View>
        <View style={styles.statCard}>
          <Text style={styles.statLabel}>Memory</Text>
          <Text style={styles.statValue}>
            {latest ? (latest.mem_rss / 1048576).toFixed(0) : "—"}
            <Text style={styles.statUnit}> MB</Text>
          </Text>
        </View>
      </View>

      {(app.ports ?? []).length > 0 && (
        <View style={styles.portsRow}>
          {(app.ports ?? []).map((p) => (
            <View key={`${p.proto}${p.port}`} style={styles.portChip}>
              <Text style={styles.portText}>
                {p.proto}:{p.port}
              </Text>
            </View>
          ))}
        </View>
      )}

      <Pressable style={styles.restart} disabled={busy} onPress={() => act("restart")}>
        <Text style={styles.restartText}>⟳ Restart</Text>
      </Pressable>

      <LogTail appId={app.id} />
    </ScrollView>
  );
}

/** Last log lines via the hub's SSE endpoint, read with fetch-streaming where
 * available (web) and XMLHttpRequest incremental text on native. Kept simple:
 * connect, accumulate, show the last 100 lines, reconnect on screen focus. */
function LogTail({ appId }: { appId: string }) {
  const [lines, setLines] = useState<string[]>([]);
  const [state, setState] = useState<"loading" | "live" | "error">("loading");

  useEffect(() => {
    const token = pb()?.authStore.token ?? "";
    const url = `${hubUrl()}/api/bb/apps/${appId}/logs?tail=100&token=${encodeURIComponent(token)}`;
    let closed = false;
    const xhr = new XMLHttpRequest();
    let seen = 0;
    xhr.open("GET", url);
    xhr.onreadystatechange = () => {
      if (closed) return;
      if (xhr.readyState >= 3 && xhr.status === 200) {
        setState("live");
        const chunk = xhr.responseText.slice(seen);
        seen = xhr.responseText.length;
        const batches = chunk
          .split("\n\n")
          .map((f) => f.trim())
          .filter((f) => f.startsWith("data: "));
        if (batches.length) {
          setLines((prev) => {
            let next = prev;
            for (const b of batches) {
              try {
                const parsed = JSON.parse(b.slice(6)) as string[];
                next = next.concat(parsed);
              } catch {
                /* keepalive or eof frame */
              }
            }
            return next.length > 100 ? next.slice(next.length - 100) : next;
          });
        }
      }
      if (xhr.readyState === 4 && xhr.status !== 200) setState("error");
    };
    xhr.onerror = () => !closed && setState("error");
    xhr.send();
    return () => {
      closed = true;
      xhr.abort();
    };
  }, [appId]);

  return (
    <View style={styles.logBox}>
      <Text style={styles.logHeader}>
        {state === "live" ? "● logs" : state === "loading" ? "logs…" : "logs unavailable (agent offline?)"}
      </Text>
      {lines.map((l, i) => (
        <Text key={i} style={styles.logLine} numberOfLines={2}>
          {l}
        </Text>
      ))}
      {state === "live" && lines.length === 0 && <Text style={styles.dim}>no output yet</Text>}
    </View>
  );
}

const styles = StyleSheet.create({
  wrap: { flex: 1, backgroundColor: colors.bg, padding: 16 },
  dim: { color: colors.faint, fontSize: 13 },
  headerRow: { flexDirection: "row", alignItems: "center" },
  dot: { width: 10, height: 10, borderRadius: 5, marginRight: 8 },
  status: { color: colors.text, fontSize: 16, fontWeight: "600" },
  pid: { color: colors.faint, fontSize: 12, marginLeft: 10 },
  warn: {
    backgroundColor: "#78350f33",
    borderColor: "#f59e0b55",
    borderWidth: 1,
    borderRadius: 10,
    padding: 10,
    marginTop: 14,
  },
  warnText: { color: colors.accent, fontSize: 13, lineHeight: 18 },
  statRow: { flexDirection: "row", gap: 12, marginTop: 16 },
  statCard: {
    flex: 1,
    backgroundColor: colors.card,
    borderColor: colors.border,
    borderWidth: 1,
    borderRadius: 12,
    padding: 14,
  },
  statLabel: { color: colors.dim, fontSize: 12 },
  statValue: { color: colors.text, fontSize: 22, fontWeight: "700", marginTop: 4 },
  statUnit: { color: colors.faint, fontSize: 13, fontWeight: "400" },
  portsRow: { flexDirection: "row", flexWrap: "wrap", gap: 8, marginTop: 14 },
  portChip: {
    backgroundColor: colors.card,
    borderColor: colors.border,
    borderWidth: 1,
    borderRadius: 6,
    paddingVertical: 3,
    paddingHorizontal: 8,
  },
  portText: { color: colors.dim, fontSize: 12 },
  restart: {
    backgroundColor: colors.card,
    borderColor: colors.border,
    borderWidth: 1,
    borderRadius: 12,
    padding: 12,
    alignItems: "center",
    marginTop: 16,
  },
  restartText: { color: colors.text, fontSize: 15 },
  logBox: {
    backgroundColor: "#000",
    borderColor: colors.border,
    borderWidth: 1,
    borderRadius: 12,
    padding: 12,
    marginTop: 16,
    marginBottom: 32,
  },
  logHeader: { color: colors.green, fontSize: 12, marginBottom: 8 },
  logLine: { color: colors.dim, fontSize: 11, fontFamily: "Menlo", lineHeight: 16 },
});
