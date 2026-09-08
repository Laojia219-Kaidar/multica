#!/usr/bin/env node
/**
 * Verify or dry-run the seven approved digital-employee display name renames.
 *
 * HiveCrew scope (this script):
 *   - Read local agent.name / description for the seven canonical UUIDs.
 *   - Optionally read HiveCosm organization projection for display_name and
 *     binding_state (multiple_active_conflict guard).
 *
 * HiveCosm scope (out of band — write-authority matrix):
 *   - Employee registry display_name must be updated through HiveCosm governed
 *     commands; IdentityBinding employee_ref / agent_ref stay fixed.
 *
 * Usage:
 *   node scripts/sync-digital-employee-display-names.mjs dry-run
 *   node scripts/sync-digital-employee-display-names.mjs verify
 *   node scripts/sync-digital-employee-display-names.mjs verify --json
 *
 * Environment (optional for HiveCosm projection readback):
 *   DATABASE_URL              — local HiveCrew Postgres (verify local agent rows)
 *   HIVECOSM_AUTHORITY_BASE_URL — HiveCosm adapter base URL
 *   HIVECOSM_AUTHORITY_BEARER_TOKEN
 *   HIVECREW_WORKSPACE_ID     — workspace UUID for X-Workspace-ID header
 *   HIVECOSM_TENANT_ID        — tenant id for adapter validation
 */

import { readFile } from "node:fs/promises";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const SPEC_PATH = resolve(__dirname, "digital-employee-display-name-renames.json");

const MULTI_CONFLICT = "multiple_active_conflict";

export async function loadRenameSpec(path = SPEC_PATH) {
  const raw = JSON.parse(await readFile(path, "utf8"));
  if (raw.schema_version !== "hivecrew.digital-employee-display-name-renames/v1") {
    throw new Error(`unsupported schema_version: ${raw.schema_version}`);
  }
  if (!Array.isArray(raw.renames) || raw.renames.length !== 7) {
    throw new Error(`expected exactly 7 renames, got ${raw.renames?.length ?? 0}`);
  }
  const ids = new Set();
  const workforceIds = new Set();
  for (const row of raw.renames) {
    for (const key of [
      "workforce_agent_id",
      "agent_id",
      "old_display_name",
      "new_display_name",
      "former_name_note",
    ]) {
      if (typeof row[key] !== "string" || row[key].trim() === "") {
        throw new Error(`rename row missing ${key}: ${JSON.stringify(row)}`);
      }
    }
    if (ids.has(row.agent_id)) throw new Error(`duplicate agent_id ${row.agent_id}`);
    if (workforceIds.has(row.workforce_agent_id)) {
      throw new Error(`duplicate workforce_agent_id ${row.workforce_agent_id}`);
    }
    ids.add(row.agent_id);
    workforceIds.add(row.workforce_agent_id);
  }
  return raw;
}

export function buildBeforeAfterTable(spec, localRows, hivecosmRows) {
  const localById = new Map((localRows ?? []).map((r) => [r.id, r]));
  const hiveByWorkforce = new Map(
    (hivecosmRows ?? []).map((r) => [r.workforce_agent_id, r]),
  );
  return spec.renames.map((rename) => {
    const local = localById.get(rename.agent_id);
    const hive = hiveByWorkforce.get(rename.workforce_agent_id);
    return {
      agent_id: rename.agent_id,
      workforce_agent_id: rename.workforce_agent_id,
      old_display_name: rename.old_display_name,
      new_display_name: rename.new_display_name,
      local_agent_name: local?.name ?? null,
      local_description: local?.description ?? null,
      local_archived: local?.archived_at != null,
      hivecosm_display_name: hive?.display_name ?? null,
      binding_state: hive?.binding_state ?? null,
      hivecrew_agent_id: hive?.hivecrew_agent_id ?? null,
      local_matches_target: local?.name === rename.new_display_name,
      hivecosm_matches_target: hive?.display_name === rename.new_display_name,
      description_has_former_note:
        typeof local?.description === "string" &&
        local.description.includes(rename.former_name_note.split(".")[0]),
    };
  });
}

export function findBindingConflicts(hivecosmRows) {
  const conflicts = [];
  for (const row of hivecosmRows ?? []) {
    if (row.binding_state === MULTI_CONFLICT) {
      conflicts.push({
        workforce_agent_id: row.workforce_agent_id,
        employee_id: row.employee_id,
        binding_state: row.binding_state,
      });
    }
  }
  return conflicts;
}

async function queryLocalAgents(databaseUrl, agentIds) {
  const { default: pg } = await import("pg");
  const client = new pg.Client({ connectionString: databaseUrl });
  await client.connect();
  try {
    const { rows } = await client.query(
      `SELECT id::text, name, description, archived_at
       FROM agent
       WHERE id = ANY($1::uuid[])`,
      [agentIds],
    );
    return rows;
  } finally {
    await client.end();
  }
}

async function fetchHiveCosmEmployees(baseUrl, token, workspaceId, tenantId) {
  const url = new URL("/api/company-ops/employees", baseUrl);
  url.searchParams.set("limit", "500");
  const response = await fetch(url, {
    headers: {
      Authorization: `Bearer ${token}`,
      "X-Workspace-ID": workspaceId,
      "X-Tenant-ID": tenantId,
      Accept: "application/json",
    },
  });
  if (!response.ok) {
    throw new Error(`HiveCosm employees GET failed: HTTP ${response.status}`);
  }
  const body = await response.json();
  const items = body.items ?? body.employees ?? [];
  return items.map((item) => ({
    employee_id: item.employee_id,
    workforce_agent_id: item.workforce_agent_id,
    display_name: item.display_name,
    binding_state: item.binding_state,
    hivecrew_agent_id: item.hivecrew_agent_id ?? null,
  }));
}

function printHumanReport(mode, table, conflicts, spec) {
  console.log(`# Digital employee display name ${mode}`);
  console.log("");
  console.log("| agent_id | workforce | old → new | local.name | hivecosm.display_name | binding_state |");
  console.log("| --- | --- | --- | --- | --- | --- |");
  for (const row of table) {
    console.log(
      `| ${row.agent_id.slice(0, 8)}… | ${row.workforce_agent_id} | ${row.old_display_name} → ${row.new_display_name} | ${row.local_agent_name ?? "(missing)"} | ${row.hivecosm_display_name ?? "(n/a)"} | ${row.binding_state ?? "(n/a)"} |`,
    );
  }
  console.log("");
  if (conflicts.length > 0) {
    console.log("FAIL: multiple_active_conflict detected:");
    for (const c of conflicts) {
      console.log(`  - ${c.workforce_agent_id} (${c.employee_id}): ${c.binding_state}`);
    }
  } else {
    console.log("OK: no multiple_active_conflict in scoped workforce rows.");
  }
  console.log("");
  console.log(`Notion refs: ${spec.notion_refs.join(", ")}`);
}

function evaluateVerify(table, conflicts, { requireHiveCosm = false } = {}) {
  const failures = [];
  for (const row of table) {
    if (row.local_archived) {
      failures.push(`${row.workforce_agent_id}: agent ${row.agent_id} is archived`);
      continue;
    }
    if (!row.local_matches_target) {
      failures.push(
        `${row.workforce_agent_id}: local agent.name is "${row.local_agent_name ?? "(missing)"}", want "${row.new_display_name}"`,
      );
    }
    if (!row.description_has_former_note) {
      failures.push(
        `${row.workforce_agent_id}: local description missing former-name note`,
      );
    }
    if (requireHiveCosm && row.hivecosm_display_name != null && !row.hivecosm_matches_target) {
      failures.push(
        `${row.workforce_agent_id}: HiveCosm display_name is "${row.hivecosm_display_name}", want "${row.new_display_name}"`,
      );
    }
    if (row.hivecrew_agent_id && row.hivecrew_agent_id !== row.agent_id) {
      failures.push(
        `${row.workforce_agent_id}: hivecrew_agent_id mismatch ${row.hivecrew_agent_id} != ${row.agent_id}`,
      );
    }
    if (row.binding_state === MULTI_CONFLICT) {
      failures.push(`${row.workforce_agent_id}: binding_state=${MULTI_CONFLICT}`);
    }
  }
  for (const c of conflicts) {
    failures.push(`conflict: ${c.workforce_agent_id} (${c.employee_id})`);
  }
  return failures;
}

async function main() {
  const mode = process.argv[2] ?? "dry-run";
  const asJson = process.argv.includes("--json");
  if (mode !== "dry-run" && mode !== "verify") {
    console.error("Usage: node scripts/sync-digital-employee-display-names.mjs <dry-run|verify> [--json]");
    process.exitCode = 2;
    return;
  }

  const spec = await loadRenameSpec();
  const agentIds = spec.renames.map((r) => r.agent_id);
  const workforceSet = new Set(spec.renames.map((r) => r.workforce_agent_id));

  let localRows = [];
  if (process.env.DATABASE_URL) {
    localRows = await queryLocalAgents(process.env.DATABASE_URL, agentIds);
  }

  let hivecosmRows = [];
  const hiveBase = process.env.HIVECOSM_AUTHORITY_BASE_URL;
  const hiveToken = process.env.HIVECOSM_AUTHORITY_BEARER_TOKEN;
  const workspaceId = process.env.HIVECREW_WORKSPACE_ID;
  const tenantId = process.env.HIVECOSM_TENANT_ID;
  if (hiveBase && hiveToken && workspaceId && tenantId) {
    const all = await fetchHiveCosmEmployees(hiveBase, hiveToken, workspaceId, tenantId);
    hivecosmRows = all.filter((r) => workforceSet.has(r.workforce_agent_id));
  }

  const table = buildBeforeAfterTable(spec, localRows, hivecosmRows);
  const conflicts = findBindingConflicts(hivecosmRows);
  const requireHiveCosm = mode === "verify" && hivecosmRows.length > 0;
  const failures =
    mode === "verify"
      ? evaluateVerify(table, conflicts, { requireHiveCosm })
      : [];

  const payload = {
    ok: failures.length === 0,
    mode,
    rename_count: spec.renames.length,
    local_agents_found: localRows.length,
    hivecosm_rows_found: hivecosmRows.length,
    multiple_active_conflicts: conflicts,
    before_after: table,
    failures,
    notion_refs: spec.notion_refs,
  };

  if (asJson) {
    console.log(JSON.stringify(payload, null, 2));
  } else {
    printHumanReport(mode, table, conflicts, spec);
    if (mode === "verify" && failures.length > 0) {
      console.log("Verification failures:");
      for (const f of failures) console.log(`  - ${f}`);
    } else if (mode === "verify") {
      console.log("Verification passed.");
    } else {
      console.log("Dry-run complete (no writes). Apply migration 260 for local agent.name updates.");
      console.log("Update HiveCosm employee registry display_name through governed commands.");
    }
  }

  if (failures.length > 0) process.exitCode = 1;
}

if (import.meta.url === new URL(process.argv[1], "file:").href) {
  main().catch((err) => {
    console.error(err);
    process.exitCode = 1;
  });
}
