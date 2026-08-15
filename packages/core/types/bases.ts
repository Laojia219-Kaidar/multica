// ---------------------------------------------------------------------------
// Observed execution bases (Lane C, P3).
//
// A "base" is a physical execution machine, derived read-only from runtime
// `device_info` machine titles plus agent-to-runtime bindings. It is a read
// model: no second source of truth is created, and company-owned home/fallback
// base assignment is a separate authority.
// ---------------------------------------------------------------------------

export interface BaseRuntimeInfo {
  runtime_id: string;
  runtime_name: string;
  status: string;
  runtime_mode: string;
  daemon_id: string;
}

export interface BaseOverview {
  machine_title: string;
  runtime_registered: number;
  runtime_online: number;
  runtime_offline: number;
  daemon_count: number;
  employees: number;
  load_running: number;
  runtimes: BaseRuntimeInfo[];
}

export interface MigrateRuntimeAgentsResponse {
  source_runtime_id: string;
  target_runtime_id: string;
  agents_migrated: number;
  tasks_migrated: number;
}
