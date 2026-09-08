import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import {
  buildBeforeAfterTable,
  findBindingConflicts,
  loadRenameSpec,
} from "./sync-digital-employee-display-names.mjs";

const __dirname = dirname(fileURLToPath(import.meta.url));
const SPEC_PATH = resolve(__dirname, "digital-employee-display-name-renames.json");

test("rename spec has seven unique agents in safe apply order", async () => {
  const spec = await loadRenameSpec(SPEC_PATH);
  assert.equal(spec.renames.length, 7);
  assert.equal(spec.apply_order.length, 7);

  const byId = new Map(spec.renames.map((r) => [r.agent_id, r]));
  for (const id of spec.apply_order) {
    assert.ok(byId.has(id), `apply_order references unknown agent_id ${id}`);
  }

  // Finn → Bolt must precede Willow → Finn to avoid workspace name collision.
  const finnPerf = "7f1d98a5-307f-40d8-893b-82d9fe09f33e";
  const willowFront = "876e2514-ee7c-4fc3-85f2-861baa464a45";
  assert.ok(
    spec.apply_order.indexOf(finnPerf) < spec.apply_order.indexOf(willowFront),
    "performance Finn must rename to Bolt before frontend Willow renames to Finn",
  );
});

test("rename spec JSON matches approved workforce ids", async () => {
  const spec = JSON.parse(await readFile(SPEC_PATH, "utf8"));
  const workforceIds = spec.renames.map((r) => r.workforce_agent_id).sort();
  assert.deepEqual(workforceIds, [
    "EXT-001",
    "HC-020",
    "KT-013",
    "KT-018",
    "KT-023",
    "KT-046",
    "KT-058",
  ]);
});

test("buildBeforeAfterTable flags local and hivecosm mismatches", () => {
  const spec = {
    renames: [
      {
        workforce_agent_id: "KT-046",
        agent_id: "7f1d98a5-307f-40d8-893b-82d9fe09f33e",
        old_display_name: "Finn｜性能优化工程师",
        new_display_name: "Bolt｜性能优化工程师",
        former_name_note: "Formerly: Finn｜性能优化工程师 (KT-046).",
      },
    ],
  };
  const table = buildBeforeAfterTable(
    spec,
    [
      {
        id: "7f1d98a5-307f-40d8-893b-82d9fe09f33e",
        name: "Bolt｜性能优化工程师",
        description: "Formerly: Finn｜性能优化工程师 (KT-046).",
        archived_at: null,
      },
    ],
    [
      {
        workforce_agent_id: "KT-046",
        employee_id: "DE-TEST",
        display_name: "Bolt｜性能优化工程师",
        binding_state: "unique_active_candidate",
        hivecrew_agent_id: "7f1d98a5-307f-40d8-893b-82d9fe09f33e",
      },
    ],
  );
  assert.equal(table.length, 1);
  assert.equal(table[0].local_matches_target, true);
  assert.equal(table[0].hivecosm_matches_target, true);
  assert.equal(table[0].description_has_former_note, true);
});

test("findBindingConflicts surfaces multiple_active_conflict", () => {
  const conflicts = findBindingConflicts([
    {
      workforce_agent_id: "KT-013",
      employee_id: "DE-1",
      binding_state: "unique_active_candidate",
    },
    {
      workforce_agent_id: "KT-023",
      employee_id: "DE-2",
      binding_state: "multiple_active_conflict",
    },
  ]);
  assert.equal(conflicts.length, 1);
  assert.equal(conflicts[0].workforce_agent_id, "KT-023");
});
