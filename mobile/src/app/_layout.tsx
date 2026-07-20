import { useEffect, useState } from "react";
import { View } from "react-native";
import { Stack, useRouter, useSegments } from "expo-router";
import { StatusBar } from "expo-status-bar";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { restoreSession, pb } from "../lib/pb";
import { colors } from "../lib/theme";

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: 1, staleTime: 5_000 } },
});

export default function RootLayout() {
  const router = useRouter();
  const segments = useSegments();
  const [ready, setReady] = useState(false);

  useEffect(() => {
    restoreSession().then((client) => {
      setReady(true);
      const inAuthFlow = segments[0] === "connect" || segments[0] === "login";
      if (!client && !inAuthFlow) {
        router.replace("/connect");
      } else if (client && !client.authStore.isValid && !inAuthFlow) {
        router.replace("/login");
      }
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Kick back to login whenever the token expires mid-session.
  useEffect(() => {
    const iv = setInterval(() => {
      const client = pb();
      if (ready && client && !client.authStore.isValid && segments[0] !== "login" && segments[0] !== "connect") {
        router.replace("/login");
      }
    }, 3000);
    return () => clearInterval(iv);
  }, [ready, segments, router]);

  // Screens must not mount before restoreSession resolves: their queries
  // capture pb() at mount, and a hard load directly on a data screen would
  // otherwise freeze with pb() still null.
  if (!ready) {
    return <View style={{ flex: 1, backgroundColor: colors.bg }} />;
  }

  return (
    <QueryClientProvider client={queryClient}>
      <StatusBar style="light" />
      <Stack
        screenOptions={{
          headerStyle: { backgroundColor: colors.bg },
          headerTintColor: colors.text,
          headerTitleStyle: { fontWeight: "700" },
          contentStyle: { backgroundColor: colors.bg },
        }}
      >
        <Stack.Screen name="index" options={{ title: "⚡ BreakerBox" }} />
        <Stack.Screen name="connect" options={{ title: "Connect to hub" }} />
        <Stack.Screen name="login" options={{ title: "Sign in" }} />
        <Stack.Screen name="app/[id]" options={{ title: "App" }} />
        <Stack.Screen name="tokens" options={{ title: "Token usage" }} />
        <Stack.Screen name="settings" options={{ title: "Settings" }} />
      </Stack>
    </QueryClientProvider>
  );
}
