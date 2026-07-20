// @breakerbox/api-client — typed PocketBase client shared by web and mobile.
// Record types mirror hub/migrations; keep in sync with the Go schema.

import PocketBase, { type RecordModel } from "pocketbase";

export type SystemStatus = "online" | "offline" | "paused";
export type AppKind = "process" | "docker" | "compose";
export type Approval = "pending" | "approved" | "rejected";
export type DesiredState = "running" | "stopped";
export type AppStatus =
  | "running"
  | "stopped"
  | "starting"
  | "backoff"
  | "errored"
  | "unknown";
export type CommandVerb = "start" | "stop" | "restart";
export type CommandStatus =
  | "pending"
  | "dispatched"
  | "acked"
  | "done"
  | "failed"
  | "timeout";

export interface SystemRecord extends RecordModel {
  name: string;
  os: string;
  arch: string;
  hostname: string;
  agent_version: string;
  status: SystemStatus;
  last_seen: string;
  capabilities: string[];
}

export interface AppDefinition {
  schema_version: number;
  name: string;
  kind?: AppKind;
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

export interface AppRecord extends RecordModel {
  system: string;
  name: string;
  kind: AppKind;
  definition: AppDefinition;
  definition_hash: string;
  approval: Approval;
  desired_state: DesiredState;
  status: AppStatus;
  pid: number;
  started_at: string;
  ports: { proto: string; port: number }[];
  token_tracking: "off" | "dev" | "runtime";
}

export interface CommandRecord extends RecordModel {
  app: string;
  system: string;
  verb: CommandVerb;
  status: CommandStatus;
  error: string;
}

export interface SystemMetricRecord extends RecordModel {
  system: string;
  type: "1m" | "10m" | "1h" | "1d";
  cpu: number;
  mem_pct: number;
  mem_used: number;
  disk_pct: number;
  net_sent: number;
  net_recv: number;
  created: string;
}

/** Creates a PocketBase client bound to a BreakerBox hub URL. */
export function createClient(hubUrl: string): PocketBase {
  return new PocketBase(hubUrl);
}

/** Sends a control command for an app; resolves with the created record. */
export async function sendCommand(
  pb: PocketBase,
  app: Pick<AppRecord, "id" | "system">,
  verb: CommandVerb,
): Promise<CommandRecord> {
  return pb.collection("commands").create<CommandRecord>({
    app: app.id,
    system: app.system,
    verb,
    status: "pending",
  });
}

export { PocketBase };
