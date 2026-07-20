import { useState } from "react";
import {
  ActivityIndicator,
  KeyboardAvoidingView,
  Platform,
  Pressable,
  StyleSheet,
  Text,
  TextInput,
} from "react-native";
import { useRouter } from "expo-router";
import { hubUrl, pb } from "../lib/pb";
import { colors } from "../lib/theme";

export default function Login() {
  const router = useRouter();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function signIn() {
    const client = pb();
    if (!client) {
      router.replace("/connect");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await client.collection("users").authWithPassword(email.trim(), password);
      router.replace("/");
    } catch {
      setError("Sign-in failed. Check your email and password.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <KeyboardAvoidingView
      style={styles.wrap}
      behavior={Platform.OS === "ios" ? "padding" : undefined}
    >
      <Text style={styles.title}>Sign in</Text>
      <Text style={styles.sub}>{hubUrl()}</Text>
      <TextInput
        style={styles.input}
        placeholder="email"
        placeholderTextColor={colors.faint}
        autoCapitalize="none"
        autoComplete="email"
        keyboardType="email-address"
        value={email}
        onChangeText={setEmail}
      />
      <TextInput
        style={styles.input}
        placeholder="password"
        placeholderTextColor={colors.faint}
        secureTextEntry
        value={password}
        onChangeText={setPassword}
        onSubmitEditing={signIn}
      />
      {error ? <Text style={styles.error}>{error}</Text> : null}
      <Pressable
        style={[styles.button, (!email || !password || busy) && styles.buttonDisabled]}
        disabled={!email || !password || busy}
        onPress={signIn}
      >
        {busy ? (
          <ActivityIndicator color={colors.accentText} />
        ) : (
          <Text style={styles.buttonText}>Sign in</Text>
        )}
      </Pressable>
      <Pressable onPress={() => router.replace("/connect")}>
        <Text style={styles.switchHub}>Use a different hub</Text>
      </Pressable>
    </KeyboardAvoidingView>
  );
}

const styles = StyleSheet.create({
  wrap: { flex: 1, backgroundColor: colors.bg, padding: 24, justifyContent: "center" },
  title: { color: colors.text, fontSize: 22, fontWeight: "700", textAlign: "center" },
  sub: { color: colors.faint, fontSize: 13, textAlign: "center", marginTop: 4 },
  input: {
    backgroundColor: colors.card,
    borderColor: colors.border,
    borderWidth: 1,
    borderRadius: 12,
    color: colors.text,
    padding: 14,
    marginTop: 12,
    fontSize: 16,
  },
  error: { color: colors.red, marginTop: 10, fontSize: 13 },
  button: {
    backgroundColor: colors.accent,
    borderRadius: 12,
    padding: 14,
    alignItems: "center",
    marginTop: 16,
  },
  buttonDisabled: { opacity: 0.4 },
  buttonText: { color: colors.accentText, fontWeight: "700", fontSize: 16 },
  switchHub: { color: colors.dim, textAlign: "center", marginTop: 20, fontSize: 14 },
});
