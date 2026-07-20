import { useEffect, useState } from "react";
import { Pressable, ScrollView, StyleSheet, Text, View } from "react-native";
import { useRouter } from "expo-router";
import { hubUrl, loadHubs, removeHub, signOut, useHub } from "../lib/pb";
import { colors } from "../lib/theme";

export default function Settings() {
  const router = useRouter();
  const [hubs, setHubs] = useState<string[]>([]);
  const active = hubUrl();

  useEffect(() => {
    loadHubs().then((h) => setHubs(h.urls));
  }, []);

  async function switchTo(url: string) {
    const client = await useHub(url);
    router.replace(client.authStore.isValid ? "/" : "/login");
  }

  async function forget(url: string) {
    await removeHub(url);
    const h = await loadHubs();
    setHubs(h.urls);
    if (url === active) router.replace("/connect");
  }

  return (
    <ScrollView style={styles.wrap}>
      <Text style={styles.section}>Hubs</Text>
      <View style={styles.card}>
        {hubs.map((u) => (
          <View key={u} style={styles.row}>
            <Pressable style={{ flex: 1 }} onPress={() => switchTo(u)}>
              <Text style={[styles.hubUrl, u === active && { color: colors.accent }]}>
                {u === active ? "● " : ""}
                {u}
              </Text>
            </Pressable>
            <Pressable onPress={() => forget(u)}>
              <Text style={styles.forget}>forget</Text>
            </Pressable>
          </View>
        ))}
      </View>
      <Pressable style={styles.addHub} onPress={() => router.push("/connect")}>
        <Text style={styles.addHubText}>+ Add hub</Text>
      </Pressable>

      <Pressable
        style={styles.signOut}
        onPress={async () => {
          await signOut();
          router.replace("/login");
        }}
      >
        <Text style={styles.signOutText}>Sign out</Text>
      </Pressable>

      <Text style={styles.hint}>
        Alerts on this phone: install the ntfy app and subscribe to the topic configured in the
        web dashboard's Settings → Notifications.
      </Text>
    </ScrollView>
  );
}

const styles = StyleSheet.create({
  wrap: { flex: 1, backgroundColor: colors.bg, padding: 16 },
  section: {
    color: colors.faint,
    fontSize: 12,
    textTransform: "uppercase",
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
    paddingVertical: 12,
    borderTopColor: colors.border,
    borderTopWidth: StyleSheet.hairlineWidth,
  },
  hubUrl: { color: colors.text, fontSize: 14 },
  forget: { color: colors.red, fontSize: 13, marginLeft: 12 },
  addHub: { marginTop: 10 },
  addHubText: { color: colors.accent, fontSize: 14 },
  signOut: {
    backgroundColor: colors.card,
    borderColor: colors.border,
    borderWidth: 1,
    borderRadius: 12,
    padding: 13,
    alignItems: "center",
    marginTop: 28,
  },
  signOutText: { color: colors.red, fontSize: 15, fontWeight: "600" },
  hint: { color: colors.faint, fontSize: 12, lineHeight: 18, marginTop: 24, marginBottom: 40 },
});
