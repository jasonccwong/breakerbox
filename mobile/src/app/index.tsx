import { useMemo } from "react";
import {
  Pressable,
  RefreshControl,
  ScrollView,
  StyleSheet,
  Switch,
  Text,
  View,
} from "react-native";
import { useRouter } from "expo-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { pb } from "../lib/pb";
import { colors, statusColor } from "../lib/theme";

interface SystemRec {
  id: string;
  name: string;
  hostname: string;
  os: string;
  status: string;
}
interface AppRec {
  id: string;
  system: string;
  name: string;
  status: string;
  approval: string;
  pid: number;
}

export default function Dashboard() {
  const router = useRouter();
  const qc = useQueryClient();

  const systems = useQuery({
    queryKey: ["systems"],
    queryFn: () => pb()!.collection("systems").getFullList<SystemRec>({ sort: "name" }),
    enabled: !!pb(),
    refetchInterval: 10_000,
  });
  const apps = useQuery({
    queryKey: ["apps"],
    queryFn: () => pb()!.collection("apps").getFullList<AppRec>({ sort: "name" }),
    enabled: !!pb(),
    refetchInterval: 5_000,
  });

  const bySystem = useMemo(() => {
    const m = new Map<string, AppRec[]>();
    for (const a of apps.data ?? []) {
      if (!m.has(a.system)) m.set(a.system, []);
      m.get(a.system)!.push(a);
    }
    return m;
  }, [apps.data]);

  async function toggle(app: AppRec, next: boolean) {
    // Optimistic status so the switch answers instantly.
    qc.setQueryData<AppRec[]>(["apps"], (prev) =>
      prev?.map((a) => (a.id === app.id ? { ...a, status: next ? "starting" : "stopped" } : a)),
    );
    try {
      await pb()!.collection("commands").create({
        app: app.id,
        system: app.system,
        verb: next ? "start" : "stop",
        status: "pending",
      });
    } finally {
      setTimeout(() => qc.invalidateQueries({ queryKey: ["apps"] }), 1500);
    }
  }

  const refreshing = systems.isRefetching || apps.isRefetching;

  return (
    <ScrollView
      style={styles.wrap}
      refreshControl={
        <RefreshControl
          refreshing={refreshing}
          onRefresh={() => {
            systems.refetch();
            apps.refetch();
          }}
          tintColor={colors.dim}
        />
      }
    >
      <View style={styles.topRow}>
        <Pressable style={styles.navBtn} onPress={() => router.push("/tokens")}>
          <Text style={styles.navBtnText}>💸 Tokens</Text>
        </Pressable>
        <Pressable style={styles.navBtn} onPress={() => router.push("/settings")}>
          <Text style={styles.navBtnText}>⚙️ Settings</Text>
        </Pressable>
      </View>

      {(systems.data ?? []).map((sys) => (
        <View key={sys.id} style={styles.card}>
          <View style={styles.sysHeader}>
            <View style={[styles.dot, { backgroundColor: sys.status === "online" ? colors.green : colors.red }]} />
            <Text style={styles.sysName}>{sys.name || sys.hostname}</Text>
            <Text style={styles.sysMeta}>{sys.os}</Text>
          </View>
          {(bySystem.get(sys.id) ?? []).map((app) => {
            const running = app.status === "running" || app.status === "starting";
            return (
              <Pressable
                key={app.id}
                style={styles.appRow}
                onPress={() => router.push(`/app/${app.id}`)}
              >
                <View style={[styles.dot, { backgroundColor: statusColor[app.status] ?? colors.faint }]} />
                <View style={styles.appNameWrap}>
                  <Text style={styles.appName}>{app.name}</Text>
                  <Text style={styles.appStatus}>{app.status}</Text>
                </View>
                <Switch
                  value={running}
                  disabled={app.approval !== "approved" || sys.status !== "online"}
                  onValueChange={(v) => toggle(app, v)}
                  trackColor={{ true: colors.accent, false: colors.border }}
                  thumbColor="#fafafa"
                />
              </Pressable>
            );
          })}
          {(bySystem.get(sys.id) ?? []).length === 0 && (
            <Text style={styles.emptyApps}>No apps on this system yet — add them from the web dashboard.</Text>
          )}
        </View>
      ))}

      {systems.data?.length === 0 && (
        <Text style={styles.empty}>
          No systems enrolled yet. Add your first machine from the web dashboard.
        </Text>
      )}
    </ScrollView>
  );
}

const styles = StyleSheet.create({
  wrap: { flex: 1, backgroundColor: colors.bg, padding: 16 },
  topRow: { flexDirection: "row", gap: 10, marginBottom: 14 },
  navBtn: {
    backgroundColor: colors.card,
    borderColor: colors.border,
    borderWidth: 1,
    borderRadius: 10,
    paddingVertical: 8,
    paddingHorizontal: 14,
  },
  navBtnText: { color: colors.text, fontSize: 14 },
  card: {
    backgroundColor: colors.card,
    borderColor: colors.border,
    borderWidth: 1,
    borderRadius: 14,
    padding: 14,
    marginBottom: 14,
  },
  sysHeader: { flexDirection: "row", alignItems: "center", marginBottom: 6 },
  dot: { width: 8, height: 8, borderRadius: 4, marginRight: 8 },
  sysName: { color: colors.text, fontSize: 16, fontWeight: "700", flex: 1 },
  sysMeta: { color: colors.faint, fontSize: 12 },
  appRow: {
    flexDirection: "row",
    alignItems: "center",
    paddingVertical: 10,
    borderTopColor: colors.border,
    borderTopWidth: StyleSheet.hairlineWidth,
  },
  appNameWrap: { flex: 1 },
  appName: { color: colors.text, fontSize: 15, fontWeight: "600" },
  appStatus: { color: colors.dim, fontSize: 12, marginTop: 1 },
  emptyApps: { color: colors.faint, fontSize: 13, paddingVertical: 8 },
  empty: { color: colors.dim, textAlign: "center", marginTop: 40, lineHeight: 20 },
});
