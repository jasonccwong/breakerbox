// PocketBase client bound to the user's hub, with SecureStore-backed auth
// persistence and multi-hub profiles. Realtime is deliberately not used on
// mobile — react-query polling is simpler and survives app backgrounding.
import PocketBase, { AsyncAuthStore } from "pocketbase";
import * as SecureStore from "expo-secure-store";
import { Platform } from "react-native";

const HUBS_KEY = "bb_hubs"; // JSON: { urls: string[], active: string }
const AUTH_KEY = "bb_auth"; // serialized PB auth for the active hub

// SecureStore is unavailable on web; fall back to localStorage there.
const storage = {
  async get(key: string): Promise<string | null> {
    if (Platform.OS === "web") return localStorage.getItem(key);
    return SecureStore.getItemAsync(key);
  },
  async set(key: string, value: string): Promise<void> {
    if (Platform.OS === "web") {
      localStorage.setItem(key, value);
      return;
    }
    await SecureStore.setItemAsync(key, value);
  },
  async delete(key: string): Promise<void> {
    if (Platform.OS === "web") {
      localStorage.removeItem(key);
      return;
    }
    await SecureStore.deleteItemAsync(key);
  },
};

export interface HubProfiles {
  urls: string[];
  active: string;
}

export async function loadHubs(): Promise<HubProfiles> {
  try {
    const raw = await storage.get(HUBS_KEY);
    if (raw) return JSON.parse(raw) as HubProfiles;
  } catch {
    /* corrupted -> reset */
  }
  return { urls: [], active: "" };
}

export async function saveHubs(h: HubProfiles): Promise<void> {
  await storage.set(HUBS_KEY, JSON.stringify(h));
}

let pbInstance: PocketBase | null = null;
let activeUrl = "";

/** The PB client for the active hub; null until a hub is chosen. */
export function pb(): PocketBase | null {
  return pbInstance;
}

export function hubUrl(): string {
  return activeUrl;
}

/** Normalizes user input like "myhub.example.com:8090" into a base URL. */
export function normalizeHubUrl(input: string): string {
  let u = input.trim().replace(/\/+$/, "");
  if (!/^https?:\/\//.test(u)) u = "https://" + u;
  return u;
}

/** Connects (or switches) to a hub. Auth for the hub is restored if saved. */
export async function useHub(url: string): Promise<PocketBase> {
  activeUrl = url;
  const hubs = await loadHubs();
  if (!hubs.urls.includes(url)) hubs.urls.push(url);
  hubs.active = url;
  await saveHubs(hubs);

  const store = new AsyncAuthStore({
    save: (serialized) => storage.set(authKey(url), serialized),
    clear: () => storage.delete(authKey(url)),
    initial: storage.get(authKey(url)).then((v) => v ?? undefined),
  });
  pbInstance = new PocketBase(url, store);
  return pbInstance;
}

function authKey(url: string): string {
  // One auth slot per hub, key-safe encoding.
  return AUTH_KEY + "_" + url.replace(/[^a-zA-Z0-9]/g, "_");
}

/** Restores the last active hub on app launch. */
export async function restoreSession(): Promise<PocketBase | null> {
  const hubs = await loadHubs();
  if (!hubs.active) return null;
  const client = await useHub(hubs.active);
  // AsyncAuthStore initial load is async; give it a tick to hydrate.
  await new Promise((r) => setTimeout(r, 50));
  return client;
}

export async function signOut(): Promise<void> {
  pbInstance?.authStore.clear();
}

export async function removeHub(url: string): Promise<void> {
  const hubs = await loadHubs();
  hubs.urls = hubs.urls.filter((u) => u !== url);
  if (hubs.active === url) hubs.active = hubs.urls[0] ?? "";
  await saveHubs(hubs);
  await storage.delete(authKey(url));
  if (activeUrl === url) {
    pbInstance = null;
    activeUrl = hubs.active;
  }
}
