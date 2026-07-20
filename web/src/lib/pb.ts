import PocketBase from "pocketbase";

// Relative base URL: works both embedded in the hub binary and behind the
// Vite dev proxy.
export const pb = new PocketBase(window.location.origin);

export type SystemStatus = "online" | "offline" | "paused";
export type AppStatus =
  | "running"
  | "stopped"
  | "starting"
  | "backoff"
  | "errored"
  | "unknown";
export type Approval = "pending" | "approved" | "rejected";

export interface SystemRecord {
  id: string;
  name: string;
  os: string;
  arch: string;
  hostname: string;
  agent_version: string;
  status: SystemStatus;
  last_seen: string;
}

export interface AppDefinition {
  schema_version: number;
  name: string;
  kind?: "process" | "docker" | "compose";
  cmd?: string;
  args?: string[];
  cwd?: string;
  env?: Record<string, string>;
  ports?: number[];
  health_check?: { url: string; timeout_s?: number };
  stop?: { command?: string; signal?: string; timeout_s?: number };
  restart_policy?: {
    max_restarts?: number;
    min_uptime_s?: number;
    backoff_max_s?: number;
  };
}

export interface AppRecord {
  id: string;
  system: string;
  name: string;
  kind: string;
  definition: AppDefinition;
  definition_hash: string;
  approval: Approval;
  desired_state: "running" | "stopped";
  status: AppStatus;
  pid: number;
  ports: { proto: string; port: number }[] | null;
}

export interface SystemMetricRecord {
  id: string;
  system: string;
  type: string;
  cpu: number;
  mem_pct: number;
  created: string;
}

export interface AppMetricRecord {
  id: string;
  app: string;
  type: string;
  cpu: number;
  mem_rss: number;
  created: string;
}

export async function sendCommand(
  app: Pick<AppRecord, "id" | "system">,
  verb: "start" | "stop" | "restart",
) {
  return pb.collection("commands").create({
    app: app.id,
    system: app.system,
    verb,
    status: "pending",
  });
}
