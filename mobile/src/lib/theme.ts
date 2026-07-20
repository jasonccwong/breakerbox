// Shared dark-theme palette (matches the web dashboard's zinc/amber look).
export const colors = {
  bg: "#09090b",
  card: "#18181bcc",
  border: "#27272a",
  text: "#e4e4e7",
  dim: "#a1a1aa",
  faint: "#52525b",
  accent: "#f59e0b",
  accentText: "#09090b",
  green: "#34d399",
  red: "#f87171",
  blue: "#60a5fa",
};

export const statusColor: Record<string, string> = {
  running: colors.green,
  starting: colors.blue,
  stopped: colors.faint,
  backoff: colors.accent,
  errored: colors.red,
  unknown: colors.faint,
};
