import { useEffect, useState } from "react";
import {
  ActivityIndicator,
  FlatList,
  KeyboardAvoidingView,
  Platform,
  Pressable,
  StyleSheet,
  Text,
  TextInput,
  View,
} from "react-native";
import { useRouter } from "expo-router";
import { loadHubs, normalizeHubUrl, useHub } from "../lib/pb";
import { colors } from "../lib/theme";

export default function Connect() {
  const router = useRouter();
  const [url, setUrl] = useState("");
  const [known, setKnown] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    loadHubs().then((h) => setKnown(h.urls));
  }, []);

  async function connect(raw: string) {
    const target = normalizeHubUrl(raw);
    setBusy(true);
    setError("");
    try {
      const res = await fetch(`${target}/api/bb/health`);
      if (!res.ok) throw new Error(`hub answered ${res.status}`);
      const client = await useHub(target);
      router.replace(client.authStore.isValid ? "/" : "/login");
    } catch (e) {
      setError(
        `Could not reach a BreakerBox hub at ${target}. Check the address and that the hub is running.`,
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <KeyboardAvoidingView
      style={styles.wrap}
      behavior={Platform.OS === "ios" ? "padding" : undefined}
    >
      <Text style={styles.logo}>⚡</Text>
      <Text style={styles.title}>Connect to your hub</Text>
      <Text style={styles.sub}>
        Enter the address of your BreakerBox hub — the same URL you open in a
        browser (e.g. https://breakerbox.example.com).
      </Text>
      <TextInput
        style={styles.input}
        placeholder="https://your-hub-url"
        placeholderTextColor={colors.faint}
        autoCapitalize="none"
        autoCorrect={false}
        keyboardType="url"
        value={url}
        onChangeText={setUrl}
        onSubmitEditing={() => url && connect(url)}
      />
      {error ? <Text style={styles.error}>{error}</Text> : null}
      <Pressable
        style={[styles.button, (!url || busy) && styles.buttonDisabled]}
        disabled={!url || busy}
        onPress={() => connect(url)}
      >
        {busy ? (
          <ActivityIndicator color={colors.accentText} />
        ) : (
          <Text style={styles.buttonText}>Connect</Text>
        )}
      </Pressable>

      {known.length > 0 && (
        <View style={styles.known}>
          <Text style={styles.knownTitle}>Saved hubs</Text>
          <FlatList
            data={known}
            keyExtractor={(u) => u}
            renderItem={({ item }) => (
              <Pressable style={styles.knownRow} onPress={() => connect(item)}>
                <Text style={styles.knownUrl}>{item}</Text>
              </Pressable>
            )}
          />
        </View>
      )}
    </KeyboardAvoidingView>
  );
}

const styles = StyleSheet.create({
  wrap: { flex: 1, backgroundColor: colors.bg, padding: 24, justifyContent: "center" },
  logo: { fontSize: 44, textAlign: "center" },
  title: { color: colors.text, fontSize: 22, fontWeight: "700", textAlign: "center", marginTop: 8 },
  sub: { color: colors.dim, fontSize: 14, textAlign: "center", marginTop: 8, lineHeight: 20 },
  input: {
    backgroundColor: colors.card,
    borderColor: colors.border,
    borderWidth: 1,
    borderRadius: 12,
    color: colors.text,
    padding: 14,
    marginTop: 20,
    fontSize: 16,
  },
  error: { color: colors.red, marginTop: 10, fontSize: 13, lineHeight: 18 },
  button: {
    backgroundColor: colors.accent,
    borderRadius: 12,
    padding: 14,
    alignItems: "center",
    marginTop: 12,
  },
  buttonDisabled: { opacity: 0.4 },
  buttonText: { color: colors.accentText, fontWeight: "700", fontSize: 16 },
  known: { marginTop: 32 },
  knownTitle: { color: colors.faint, fontSize: 12, textTransform: "uppercase", marginBottom: 8 },
  knownRow: {
    backgroundColor: colors.card,
    borderColor: colors.border,
    borderWidth: 1,
    borderRadius: 10,
    padding: 12,
    marginBottom: 8,
  },
  knownUrl: { color: colors.text, fontSize: 14 },
});
