/**
 * HIVECREW-WECHAT-REAL-OPERATIONS-V1 / WO-10R — versioned WeChat content
 * production node contract (contract-freeze only).
 *
 * This module is a pure, side-effect-free contract surface. Every exported
 * value is a TypeScript type, a frozen constant, or a pure function. It
 * creates NO Task, Run, Artifact, Outcome, queue, table, registry, or
 * second authority: the existing Task/Run + CompanyOps Artifact/Outcome
 * authorities remain the only execution and promotion path.
 *
 * Caller-supplied execution/artifact/outcome proof (task_id, run_id,
 * execution_receipt, candidate, outcome, input_digest, ...) is never
 * accepted as authority here. Those are server-issued receipts; this module
 * fails closed on them — recursively, at any nesting depth.
 */

/** Frozen contract version. Any other value must fail closed. */
export const WECHAT_CONTENT_CONTRACT_SCHEMA_VERSION =
  "hivecrew.wechat-content-node-contract.v1" as const;

/** Frozen production-request DTO version. Any other value must fail closed. */
export const WECHAT_CONTENT_PRODUCTION_REQUEST_SCHEMA_VERSION =
  "hivecrew.wechat-content-production-request.v1" as const;

/** The single channel this template owns. */
export const WECHAT_CONTENT_CHANNEL = "wechat" as const;

/**
 * Maximum handoff_note size in UTF-8 bytes (32 KiB). Mirrors the existing
 * CompanyOps assignment handler (server/internal/handler/companyops.go:
 * handoff_note must describe the work and is capped at 32 << 10 bytes).
 */
export const WECHAT_CONTENT_HANDOFF_NOTE_MAX_BYTES = 32 * 1024;

/**
 * Canonical HiveCosm work-order source ref shape. Mirrors
 * server/internal/companyops/hivecosm_authority_client.go. The captured
 * `{project}` segment must equal the request's own `project_id`, otherwise
 * the request fails closed as cross-project.
 */
export const WECHAT_WORK_ORDER_SOURCE_REF_PATTERN =
  /^hive:\/\/hivecosm\/delivery\/project\/([A-Za-z0-9][A-Za-z0-9@._:-]{0,191})\/work-order\/[A-Za-z0-9][A-Za-z0-9@._:-]{0,191}$/;

/** Local authority identifier shape (HiveCrew agent / runtime session UUID). */
export const UUID_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Immutable definition-version binding digest must be sha256:{64 hex}. */
export const SHA256_DIGEST_PATTERN = /^sha256:[0-9a-f]{64}$/;

/**
 * RFC3339 datetime shape (with timezone) required for the production
 * deadline. Both `Z` and numeric offsets (`+08:00`) are legal; the calendar
 * components are additionally validated by {@link isValidRfc3339Datetime}
 * so all three layers (TS / Zod / Go) accept and reject the same values.
 */
export const ISO_DATETIME_PATTERN =
  /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]\d{2}:\d{2})$/;

const RFC3339_FULL_PATTERN =
  /^(?<year>\d{4})-(?<month>\d{2})-(?<day>\d{2})T(?<hour>\d{2}):(?<minute>\d{2}):(?<second>\d{2})(?:\.\d{1,9})?(?<tz>Z|(?<offsetSign>[+-])(?<offsetHour>\d{2}):(?<offsetMinute>\d{2}))$/;

/**
 * Strict RFC3339 validation: shape (with mandatory timezone) plus real
 * calendar components. Matches Go `time.Parse(time.RFC3339Nano, ...)`
 * semantics (no leap seconds, offset hour 00-23, offset minute 00-59).
 */
export function isValidRfc3339Datetime(value: string): boolean {
  const match = RFC3339_FULL_PATTERN.exec(value);
  if (!match || !match.groups) return false;
  const year = Number(match.groups.year);
  const month = Number(match.groups.month);
  const day = Number(match.groups.day);
  const hour = Number(match.groups.hour);
  const minute = Number(match.groups.minute);
  const second = Number(match.groups.second);
  if (month < 1 || month > 12) return false;
  if (day < 1 || day > 31) return false;
  if (hour > 23 || minute > 59 || second > 59) return false;
  const tz = match.groups.tz as string;
  if (tz !== "Z") {
    const offsetHour = Number(match.groups.offsetHour);
    const offsetMinute = Number(match.groups.offsetMinute);
    if (offsetHour > 23 || offsetMinute > 59) return false;
  }
  // Real-calendar check: rejects 2026-02-30, 2026-13-01, etc.
  const utc = new Date(Date.UTC(year, month - 1, day, hour, minute, second));
  return (
    utc.getUTCFullYear() === year &&
    utc.getUTCMonth() === month - 1 &&
    utc.getUTCDate() === day &&
    utc.getUTCHours() === hour &&
    utc.getUTCMinutes() === minute &&
    utc.getUTCSeconds() === second
  );
}

/** UTF-8 byte length, mirroring Go `len(string)` semantics. */
export function wechatContentUtf8ByteLength(value: string): number {
  return new TextEncoder().encode(value).length;
}

/**
 * The four immutable content-production node keys, in prerequisite order.
 * This set is frozen: adding, removing, renaming, or reordering a node is a
 * new contract version, never a silent mutation.
 */
export const WECHAT_CONTENT_NODE_KEYS = [
  "research-material-package",
  "article-draft",
  "editorial-review-report",
  "wechat-publication-package",
] as const;

export type WechatContentNodeKey = (typeof WECHAT_CONTENT_NODE_KEYS)[number];

/** Per-node review rule. */
export const WECHAT_CONTENT_REVIEW_RULES = [
  "auto_accept",
  "editorial_review",
  "approval_gate",
  "owner_approval",
] as const;

export type WechatContentReviewRule =
  (typeof WECHAT_CONTENT_REVIEW_RULES)[number];

/** Request-level approval policy. owner_approval is the fail-closed default. */
export const WECHAT_CONTENT_APPROVAL_POLICIES = [
  "owner_approval",
  "editorial_review",
] as const;

export type WechatContentApprovalPolicy =
  (typeof WECHAT_CONTENT_APPROVAL_POLICIES)[number];

/**
 * Keys a caller must NEVER supply. These are server-issued execution /
 * artifact / outcome receipt fields, plus `input_digest` (the server computes
 * it from the exact handoff note; browsers never supply or choose an
 * authority digest). A request or node plan carrying any of them — at any
 * nesting depth — fails closed: caller refs never prove authority.
 */
export const WECHAT_CONTENT_FORBIDDEN_CALLER_PROOF_KEYS = [
  "task_id",
  "run_id",
  "initial_task_id",
  "current_task_id",
  "execution_receipt",
  "execution_state",
  "assignment_id",
  "candidate_id",
  "artifact_id",
  "formal_artifact_ref",
  "outcome_id",
  "input_digest",
] as const;

const FORBIDDEN_CALLER_PROOF_KEY_SET: ReadonlySet<string> = new Set(
  WECHAT_CONTENT_FORBIDDEN_CALLER_PROOF_KEYS,
);

/**
 * Frozen lineage authority metadata. Each lineage member's authority is a
 * contract constant, never a caller-chosen string: it names the EXISTING
 * authority that owns that lineage member.
 */
export const WECHAT_CONTENT_LINEAGE_AUTHORITIES = Object.freeze({
  issue:
    "existing Issue authority (issue table / server/migrations/001_init.up.sql)",
  assignment: "CompanyOps assignment (Dispatch -> agent_task_queue)",
  task: "agent_task_queue canonical Task",
  run: "agent_runtime canonical Run",
  candidate: "CompanyOps ArtifactCandidate (MaterializeCompletedTask)",
  outcome: "CompanyOps Outcome (promotion + readback)",
} as const);

export type WechatContentLineageMemberKey =
  keyof typeof WECHAT_CONTENT_LINEAGE_AUTHORITIES;

export type WechatContentLineageAuthority =
  (typeof WECHAT_CONTENT_LINEAGE_AUTHORITIES)[WechatContentLineageMemberKey];

/** Existing CompanyOps authority-context refs. These identify — they do not
 * authorize. Authority is resolved server-side (P0-GATE-02). */
export type WechatContentAuthorityContext = {
  work_order_source_ref: string;
  employee_id: string;
  identity_binding_id: string;
  agent_id: string;
  session_id: string;
};

/** Immutable published-definition version binding. */
export type WechatContentDefinitionBinding = {
  definition_id: string;
  version: number;
  digest: string;
};

/**
 * Content brief. `handoff_note` is the exact work description delivered to
 * the executing Agent; it is required (trimmed non-empty, max 32 KiB UTF-8)
 * and matches the existing CompanyOps assignment Handoff semantics. The
 * server computes `input_digest` from it; callers never supply one.
 */
export type WechatContentBrief = {
  subject: string;
  objective: string;
  audience: string;
  source_refs: string[];
  tone: string;
  deadline: string;
  approval_policy: WechatContentApprovalPolicy;
  handoff_note: string;
};

/**
 * Production request. It references project/work-order sources, a frozen
 * published definition version, and a content brief. It carries NO execution
 * or artifact proof: those are server-issued receipts only.
 */
export type WechatContentProductionRequest = {
  schema_version: typeof WECHAT_CONTENT_PRODUCTION_REQUEST_SCHEMA_VERSION;
  channel: typeof WECHAT_CONTENT_CHANNEL;
  project_id: string;
  authority: WechatContentAuthorityContext;
  definition: WechatContentDefinitionBinding;
  brief: WechatContentBrief;
  idempotency_key: string;
};

/** One member of a node's future durable lineage. */
export type WechatContentLineageMember = {
  required: true;
  authority: WechatContentLineageAuthority;
};

/**
 * Future per-node durable lineage shape. Each of the four content nodes owns
 * its OWN Issue/Assignment/Task/Run/Candidate/Outcome lineage — a single
 * Task materializing a single Candidate does NOT satisfy four nodes.
 */
export type WechatContentNodeLineageShape = {
  issue: WechatContentLineageMember;
  assignment: WechatContentLineageMember;
  task: WechatContentLineageMember;
  run: WechatContentLineageMember;
  candidate: WechatContentLineageMember;
  outcome: WechatContentLineageMember;
};

/** One frozen, immutable node contract. */
export type WechatContentNodeContract = {
  key: WechatContentNodeKey;
  order: number;
  requiredUpstream: WechatContentNodeKey | null;
  artifactKind: string;
  reviewRule: WechatContentReviewRule;
  lineage: WechatContentNodeLineageShape;
};

const NODE_LINEAGE_SHAPE: WechatContentNodeLineageShape = Object.freeze({
  issue: Object.freeze({
    required: true as const,
    authority: WECHAT_CONTENT_LINEAGE_AUTHORITIES.issue,
  }),
  assignment: Object.freeze({
    required: true as const,
    authority: WECHAT_CONTENT_LINEAGE_AUTHORITIES.assignment,
  }),
  task: Object.freeze({
    required: true as const,
    authority: WECHAT_CONTENT_LINEAGE_AUTHORITIES.task,
  }),
  run: Object.freeze({
    required: true as const,
    authority: WECHAT_CONTENT_LINEAGE_AUTHORITIES.run,
  }),
  candidate: Object.freeze({
    required: true as const,
    authority: WECHAT_CONTENT_LINEAGE_AUTHORITIES.candidate,
  }),
  outcome: Object.freeze({
    required: true as const,
    authority: WECHAT_CONTENT_LINEAGE_AUTHORITIES.outcome,
  }),
});

/**
 * The four immutable node contracts, frozen. Duplicate or altered nodes,
 * unknown nodes, missing nodes, and broken prerequisites all fail closed
 * against this table.
 */
export const WECHAT_CONTENT_NODE_CONTRACTS: Readonly<
  Record<WechatContentNodeKey, WechatContentNodeContract>
> = Object.freeze({
  "research-material-package": Object.freeze({
    key: "research-material-package",
    order: 1,
    requiredUpstream: null,
    artifactKind: "wechat.research-material-package.v1",
    reviewRule: "auto_accept",
    lineage: NODE_LINEAGE_SHAPE,
  }),
  "article-draft": Object.freeze({
    key: "article-draft",
    order: 2,
    requiredUpstream: "research-material-package",
    artifactKind: "wechat.article-draft.v1",
    reviewRule: "editorial_review",
    lineage: NODE_LINEAGE_SHAPE,
  }),
  "editorial-review-report": Object.freeze({
    key: "editorial-review-report",
    order: 3,
    requiredUpstream: "article-draft",
    artifactKind: "wechat.editorial-review-report.v1",
    reviewRule: "approval_gate",
    lineage: NODE_LINEAGE_SHAPE,
  }),
  "wechat-publication-package": Object.freeze({
    key: "wechat-publication-package",
    order: 4,
    requiredUpstream: "editorial-review-report",
    artifactKind: "wechat.wechat-publication-package.v1",
    reviewRule: "owner_approval",
    lineage: NODE_LINEAGE_SHAPE,
  }),
});

/** The full frozen production contract (version + channel + four nodes). */
export type WechatContentProductionContract = {
  schema_version: typeof WECHAT_CONTENT_CONTRACT_SCHEMA_VERSION;
  channel: typeof WECHAT_CONTENT_CHANNEL;
  nodes: readonly WechatContentNodeContract[];
};

/** Read-only canonical node plan (the frozen four-node sequence). */
export function wechatContentCanonicalNodePlan(): readonly WechatContentNodeContract[] {
  return WECHAT_CONTENT_NODE_KEYS.map(
    (key) => WECHAT_CONTENT_NODE_CONTRACTS[key],
  );
}

/** Read-only frozen production contract (immutable version binding). */
export function wechatContentProductionContract(): WechatContentProductionContract {
  return {
    schema_version: WECHAT_CONTENT_CONTRACT_SCHEMA_VERSION,
    channel: WECHAT_CONTENT_CHANNEL,
    nodes: wechatContentCanonicalNodePlan(),
  };
}

/**
 * Stable per-node lineage keys. Each node owns its own namespaced lineage so
 * one Task -> one Candidate can never stand in for four node artifacts.
 */
export function wechatContentNodeLineageKeys(nodeKey: WechatContentNodeKey): {
  issue: string;
  assignment: string;
  task: string;
  run: string;
  candidate: string;
  outcome: string;
} {
  const ns = `wechat-content-node:${nodeKey}`;
  return {
    issue: `${ns}:issue`,
    assignment: `${ns}:assignment`,
    task: `${ns}:task`,
    run: `${ns}:run`,
    candidate: `${ns}:candidate`,
    outcome: `${ns}:outcome`,
  };
}

/** Stable validation issue. */
export type WechatContentContractIssue = {
  code: string;
  message: string;
  path?: string[];
};

export type WechatContentContractValidationResult<T = unknown> =
  | { ok: true; value: T }
  | { ok: false; issues: WechatContentContractIssue[] };

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function issue(
  code: string,
  message: string,
  path?: string[],
): WechatContentContractIssue {
  return path ? { code, message, path } : { code, message };
}

function stringField(
  record: Record<string, unknown>,
  key: string,
): string | undefined {
  const value = record[key];
  return typeof value === "string" ? value : undefined;
}

/**
 * Recursive forged-proof scan. Any forbidden caller-proof key found at ANY
 * nesting depth (objects and arrays) fails closed. This is the TS counterpart
 * of the Go raw-JSON deep scan: neither layer may silently drop a nested
 * forged proof.
 */
function scanForbiddenProofKeysDeep(
  value: unknown,
  issues: WechatContentContractIssue[],
  basePath: string[],
): void {
  if (Array.isArray(value)) {
    value.forEach((item, index) =>
      scanForbiddenProofKeysDeep(item, issues, [...basePath, String(index)]),
    );
    return;
  }
  if (!isRecord(value)) return;
  for (const [key, item] of Object.entries(value)) {
    if (FORBIDDEN_CALLER_PROOF_KEY_SET.has(key)) {
      issues.push(
        issue(
          "caller_supplied_execution_proof",
          `caller-supplied ${key} is server-issued execution/artifact/outcome proof; caller refs never prove authority`,
          [...basePath, key],
        ),
      );
    }
    scanForbiddenProofKeysDeep(item, issues, [...basePath, key]);
  }
}

/** Fail-closed unknown-key check for one known contract section. */
function scanUnknownKeys(
  record: Record<string, unknown>,
  allowedKeys: ReadonlySet<string>,
  issues: WechatContentContractIssue[],
  basePath: string[],
): void {
  for (const key of Object.keys(record)) {
    if (!allowedKeys.has(key) && !FORBIDDEN_CALLER_PROOF_KEY_SET.has(key)) {
      issues.push(
        issue("unknown_field", `unknown field ${key}`, [...basePath, key]),
      );
    }
  }
}

const AUTHORITY_ALLOWED_KEYS: ReadonlySet<string> = new Set([
  "work_order_source_ref",
  "employee_id",
  "identity_binding_id",
  "agent_id",
  "session_id",
]);

const DEFINITION_ALLOWED_KEYS: ReadonlySet<string> = new Set([
  "definition_id",
  "version",
  "digest",
]);

const BRIEF_ALLOWED_KEYS: ReadonlySet<string> = new Set([
  "subject",
  "objective",
  "audience",
  "source_refs",
  "tone",
  "deadline",
  "approval_policy",
  "handoff_note",
]);

const LINEAGE_MEMBER_KEYS: readonly WechatContentLineageMemberKey[] = [
  "issue",
  "assignment",
  "task",
  "run",
  "candidate",
  "outcome",
];

const LINEAGE_MEMBER_ALLOWED_KEYS: ReadonlySet<string> = new Set([
  "required",
  "authority",
]);

function projectFromWorkOrderSourceRef(
  workOrderSourceRef: string,
): string | null {
  const match = WECHAT_WORK_ORDER_SOURCE_REF_PATTERN.exec(workOrderSourceRef);
  return match ? (match[1] ?? null) : null;
}

/**
 * Fail-closed validation of a WeChat content production request. Validates
 * (in order): object shape, forbidden caller proof keys (recursively),
 * unknown fields (top level and inside authority/definition/brief), schema
 * version, channel, project ref, authority refs (absent/unknown/
 * cross-project), immutable definition-version binding, brief fields
 * (including the required handoff note), and idempotency key.
 */
export function validateWechatContentProductionRequest(
  input: unknown,
): WechatContentContractValidationResult<WechatContentProductionRequest> {
  const issues: WechatContentContractIssue[] = [];
  if (!isRecord(input)) {
    return {
      ok: false,
      issues: [issue("not_an_object", "request must be an object")],
    };
  }

  scanForbiddenProofKeysDeep(input, issues, []);

  const allowedTopLevelKeys = new Set([
    "schema_version",
    "channel",
    "project_id",
    "authority",
    "definition",
    "brief",
    "idempotency_key",
  ]);
  scanUnknownKeys(input, allowedTopLevelKeys, issues, []);

  const schemaVersion = stringField(input, "schema_version");
  if (schemaVersion !== WECHAT_CONTENT_PRODUCTION_REQUEST_SCHEMA_VERSION) {
    issues.push(
      issue(
        "unsupported_schema_version",
        `unsupported schema_version; expected ${WECHAT_CONTENT_PRODUCTION_REQUEST_SCHEMA_VERSION}`,
        ["schema_version"],
      ),
    );
  }

  const channel = stringField(input, "channel");
  if (channel !== WECHAT_CONTENT_CHANNEL) {
    issues.push(
      issue("unsupported_channel", `unsupported channel; expected ${WECHAT_CONTENT_CHANNEL}`, ["channel"]),
    );
  }

  const projectId = stringField(input, "project_id");
  if (!projectId || projectId.trim().length === 0) {
    issues.push(
      issue("missing_project_id", "project_id is required", ["project_id"]),
    );
  }

  const authority = input.authority;
  if (!isRecord(authority)) {
    issues.push(
      issue("missing_authority_context", "authority context is required", ["authority"]),
    );
  } else {
    scanUnknownKeys(authority, AUTHORITY_ALLOWED_KEYS, issues, ["authority"]);
    const workOrderSourceRef = stringField(authority, "work_order_source_ref");
    if (!workOrderSourceRef || workOrderSourceRef.trim().length === 0) {
      issues.push(
        issue("invalid_authority_field", "work_order_source_ref is required", ["authority", "work_order_source_ref"]),
      );
    } else if (!WECHAT_WORK_ORDER_SOURCE_REF_PATTERN.test(workOrderSourceRef)) {
      issues.push(
        issue(
          "invalid_authority_field",
          "work_order_source_ref must be hive://hivecosm/delivery/project/{project}/work-order/{work-order}",
          ["authority", "work_order_source_ref"],
        ),
      );
    } else if (
      projectId &&
      projectFromWorkOrderSourceRef(workOrderSourceRef) !== projectId
    ) {
      issues.push(
        issue(
          "cross_project_authority_mismatch",
          "work_order_source_ref project does not match request project_id",
          ["authority", "work_order_source_ref"],
        ),
      );
    }

    const employeeId = stringField(authority, "employee_id");
    if (!employeeId || employeeId.trim().length === 0) {
      issues.push(
        issue("invalid_authority_field", "employee_id is required", ["authority", "employee_id"]),
      );
    }
    const identityBindingId = stringField(authority, "identity_binding_id");
    if (!identityBindingId || identityBindingId.trim().length === 0) {
      issues.push(
        issue("invalid_authority_field", "identity_binding_id is required", ["authority", "identity_binding_id"]),
      );
    }
    const agentId = stringField(authority, "agent_id");
    if (!agentId || !UUID_PATTERN.test(agentId)) {
      issues.push(
        issue("invalid_authority_field", "agent_id must be a UUID", ["authority", "agent_id"]),
      );
    }
    const sessionId = stringField(authority, "session_id");
    if (!sessionId || !UUID_PATTERN.test(sessionId)) {
      issues.push(
        issue("invalid_authority_field", "session_id must be a UUID", ["authority", "session_id"]),
      );
    }
  }

  const definition = input.definition;
  if (!isRecord(definition)) {
    issues.push(
      issue("missing_definition_binding", "definition binding is required", ["definition"]),
    );
  } else {
    scanUnknownKeys(definition, DEFINITION_ALLOWED_KEYS, issues, ["definition"]);
    const definitionId = stringField(definition, "definition_id");
    if (!definitionId || definitionId.trim().length === 0) {
      issues.push(
        issue("invalid_definition_binding", "definition_id is required", ["definition", "definition_id"]),
      );
    }
    const version = definition.version;
    if (typeof version !== "number" || !Number.isInteger(version) || version < 1) {
      issues.push(
        issue("invalid_definition_binding", "version must be a positive integer", ["definition", "version"]),
      );
    }
    const digest = stringField(definition, "digest");
    if (!digest || !SHA256_DIGEST_PATTERN.test(digest)) {
      issues.push(
        issue("invalid_definition_binding", "digest must be sha256:{64 hex}", ["definition", "digest"]),
      );
    }
  }

  const brief = input.brief;
  if (!isRecord(brief)) {
    issues.push(
      issue("missing_brief", "brief is required", ["brief"]),
    );
  } else {
    scanUnknownKeys(brief, BRIEF_ALLOWED_KEYS, issues, ["brief"]);
    for (const field of ["subject", "objective", "audience", "tone"] as const) {
      const value = stringField(brief, field);
      if (!value || value.trim().length === 0) {
        issues.push(
          issue("invalid_brief_field", `${field} is required`, ["brief", field]),
        );
      }
    }
    const sourceRefs = brief.source_refs;
    if (
      !Array.isArray(sourceRefs) ||
      sourceRefs.length === 0 ||
      sourceRefs.some((ref) => typeof ref !== "string" || ref.trim().length === 0)
    ) {
      issues.push(
        issue("invalid_brief_field", "source_refs must contain at least one non-empty string", ["brief", "source_refs"]),
      );
    }
    const deadline = stringField(brief, "deadline");
    if (!deadline || !isValidRfc3339Datetime(deadline)) {
      issues.push(
        issue("invalid_brief_field", "deadline must be a valid RFC3339 datetime with timezone (Z or numeric offset)", ["brief", "deadline"]),
      );
    }
    const approvalPolicy = stringField(brief, "approval_policy");
    if (
      !approvalPolicy ||
      !(WECHAT_CONTENT_APPROVAL_POLICIES as readonly string[]).includes(approvalPolicy)
    ) {
      issues.push(
        issue("invalid_brief_field", `approval_policy must be one of ${WECHAT_CONTENT_APPROVAL_POLICIES.join(", ")}`, ["brief", "approval_policy"]),
      );
    }
    const handoffNote = stringField(brief, "handoff_note");
    if (!handoffNote || handoffNote.trim().length === 0) {
      issues.push(
        issue("invalid_brief_field", "handoff_note is required and must describe the work to dispatch", ["brief", "handoff_note"]),
      );
    } else if (
      wechatContentUtf8ByteLength(handoffNote) > WECHAT_CONTENT_HANDOFF_NOTE_MAX_BYTES
    ) {
      issues.push(
        issue("invalid_brief_field", `handoff_note must be at most ${WECHAT_CONTENT_HANDOFF_NOTE_MAX_BYTES} UTF-8 bytes`, ["brief", "handoff_note"]),
      );
    }
  }

  const idempotencyKey = stringField(input, "idempotency_key");
  if (!idempotencyKey || idempotencyKey.trim().length === 0) {
    issues.push(
      issue("missing_idempotency_key", "idempotency_key is required", ["idempotency_key"]),
    );
  }

  if (issues.length > 0) {
    return { ok: false, issues };
  }
  return { ok: true, value: input as unknown as WechatContentProductionRequest };
}

/** A caller-submitted node plan entry (validated against the frozen table). */
export type WechatContentNodePlanEntry = {
  key: string;
  artifact_kind?: string;
  required_upstream?: string | null;
  review_rule?: string;
  [key: string]: unknown;
};

/**
 * Fail-closed lineage validation. The lineage must be EXACTLY the six frozen
 * members (issue/assignment/task/run/candidate/outcome); each member must be
 * exactly `{required: true, authority: <frozen contract constant>}`. Missing,
 * extra, or re-valued members/authorities all fail closed — lineage authority
 * metadata is a contract constant, never a caller-chosen string.
 */
function validateLineageShape(
  value: unknown,
  issues: WechatContentContractIssue[],
  path: string[],
): void {
  if (!isRecord(value)) {
    issues.push(
      issue("altered_node", "lineage must be the six-member Issue/Assignment/Task/Run/Candidate/Outcome shape", path),
    );
    return;
  }
  for (const key of Object.keys(value)) {
    if (!(LINEAGE_MEMBER_KEYS as readonly string[]).includes(key)) {
      issues.push(
        issue("unknown_field", `unknown lineage member ${key}`, [...path, key]),
      );
    }
  }
  for (const member of LINEAGE_MEMBER_KEYS) {
    const entry = value[member];
    const memberPath = [...path, member];
    if (!isRecord(entry)) {
      issues.push(
        issue("altered_node", `lineage member ${member} is required`, memberPath),
      );
      continue;
    }
    scanUnknownKeys(entry, LINEAGE_MEMBER_ALLOWED_KEYS, issues, memberPath);
    if (entry.required !== true) {
      issues.push(
        issue("altered_node", `lineage member ${member} required must be true`, memberPath),
      );
    }
    const frozenAuthority = WECHAT_CONTENT_LINEAGE_AUTHORITIES[member];
    if (entry.authority !== frozenAuthority) {
      issues.push(
        issue(
          "altered_node",
          `lineage member ${member} authority is the frozen contract constant, not a caller-chosen string`,
          [...memberPath, "authority"],
        ),
      );
    }
  }
}

/**
 * Fail-closed validation of a node plan against the frozen four-node
 * contract. Rejects: duplicate/unknown/missing/altered nodes, broken
 * prerequisites, wrong order, unsupported schema version, non-frozen lineage,
 * and any caller-supplied execution/artifact/outcome proof (recursively).
 */
export function validateWechatContentNodePlan(
  input: unknown,
): WechatContentContractValidationResult<readonly WechatContentNodeContract[]> {
  if (!Array.isArray(input)) {
    return {
      ok: false,
      issues: [issue("not_an_array", "node plan must be an array")],
    };
  }
  if (input.length === 0) {
    return {
      ok: false,
      issues: [issue("empty_node_plan", "node plan must contain the four frozen nodes")],
    };
  }

  const issues: WechatContentContractIssue[] = [];
  const seen = new Map<string, number>();

  input.forEach((rawEntry, index) => {
    const path = [String(index)];
    if (!isRecord(rawEntry)) {
      issues.push(issue("not_an_object", `node plan entry ${index} must be an object`, path));
      return;
    }
    scanForbiddenProofKeysDeep(rawEntry, issues, path);

    const allowedEntryKeys = new Set([
      "key",
      "order",
      "artifact_kind",
      "required_upstream",
      "review_rule",
      "lineage",
      "schema_version",
    ]);
    scanUnknownKeys(rawEntry, allowedEntryKeys, issues, path);

    const schemaVersion = stringField(rawEntry, "schema_version");
    if (
      schemaVersion !== undefined &&
      schemaVersion !== WECHAT_CONTENT_CONTRACT_SCHEMA_VERSION
    ) {
      issues.push(
        issue("unsupported_schema_version", `unsupported schema_version; expected ${WECHAT_CONTENT_CONTRACT_SCHEMA_VERSION}`, [...path, "schema_version"]),
      );
    }

    const key = stringField(rawEntry, "key");
    if (!key) {
      issues.push(issue("unknown_node", `node plan entry ${index} is missing a key`, path));
      return;
    }
    if (!(WECHAT_CONTENT_NODE_KEYS as readonly string[]).includes(key)) {
      issues.push(issue("unknown_node", `unknown node key ${key}`, [...path, "key"]));
      return;
    }
    const nodeKey = key as WechatContentNodeKey;
    if (seen.has(nodeKey)) {
      issues.push(issue("duplicate_node", `duplicate node ${key}`, [...path, "key"]));
      return;
    }
    seen.set(nodeKey, index);

    const frozen = WECHAT_CONTENT_NODE_CONTRACTS[nodeKey];
    const artifactKind = stringField(rawEntry, "artifact_kind");
    if (artifactKind !== undefined && artifactKind !== frozen.artifactKind) {
      issues.push(issue("altered_node", `node ${key} artifact_kind altered from ${frozen.artifactKind}`, [...path, "artifact_kind"]));
    }
    const requiredUpstream = rawEntry.required_upstream;
    if (
      requiredUpstream !== undefined &&
      requiredUpstream !== frozen.requiredUpstream
    ) {
      issues.push(issue("altered_node", `node ${key} required_upstream altered from ${String(frozen.requiredUpstream)}`, [...path, "required_upstream"]));
    }
    const reviewRule = stringField(rawEntry, "review_rule");
    if (reviewRule !== undefined && reviewRule !== frozen.reviewRule) {
      issues.push(issue("altered_node", `node ${key} review_rule altered from ${frozen.reviewRule}`, [...path, "review_rule"]));
    }
    const order = rawEntry.order;
    if (order !== undefined && order !== frozen.order) {
      issues.push(issue("altered_node", `node ${key} order altered from ${frozen.order}`, [...path, "order"]));
    }
    if (rawEntry.lineage !== undefined) {
      validateLineageShape(rawEntry.lineage, issues, [...path, "lineage"]);
    }
  });

  // Missing nodes.
  for (const key of WECHAT_CONTENT_NODE_KEYS) {
    if (!seen.has(key)) {
      issues.push(issue("missing_node", `missing frozen node ${key}`, ["<missing>"]));
    }
  }

  // Prerequisite order is checked against the SUBMITTED order (not the
  // canonical order), so a reordered plan fails closed.
  const submittedKeys = [...seen.entries()]
    .sort((a, b) => a[1] - b[1])
    .map(([key]) => key);
  for (let i = 0; i < submittedKeys.length; i += 1) {
    const nodeKey = submittedKeys[i] as WechatContentNodeKey;
    const frozen = WECHAT_CONTENT_NODE_CONTRACTS[nodeKey];
    if (frozen.requiredUpstream !== null) {
      const upstreamIndex = submittedKeys.indexOf(frozen.requiredUpstream);
      if (upstreamIndex === -1) {
        issues.push(
          issue("broken_prerequisite", `node ${nodeKey} is missing its upstream ${frozen.requiredUpstream}`, [nodeKey]),
        );
      } else if (upstreamIndex >= i) {
        issues.push(
          issue("broken_prerequisite", `node ${nodeKey} precedes its upstream ${frozen.requiredUpstream}`, [nodeKey]),
        );
      }
    }
  }

  if (issues.length > 0) {
    return { ok: false, issues };
  }
  return { ok: true, value: wechatContentCanonicalNodePlan() };
}

/**
 * Deterministic idempotency fingerprint for a validated request. A replayed
 * request with the same idempotency_key MUST produce an identical fingerprint;
 * the same key with a different fingerprint is a conflict and must fail
 * closed. This is a canonical serialization for replay-equality, not a
 * cryptographic digest, and it never creates a Task/Run/Artifact/Outcome.
 */
export function wechatContentRequestIdempotencyFingerprint(
  request: WechatContentProductionRequest,
): string {
  const canonical = {
    schema_version: request.schema_version,
    channel: request.channel,
    project_id: request.project_id,
    authority: {
      work_order_source_ref: request.authority.work_order_source_ref,
      employee_id: request.authority.employee_id,
      identity_binding_id: request.authority.identity_binding_id,
      agent_id: request.authority.agent_id,
      session_id: request.authority.session_id,
    },
    definition: {
      definition_id: request.definition.definition_id,
      version: request.definition.version,
      digest: request.definition.digest,
    },
    brief: {
      subject: request.brief.subject,
      objective: request.brief.objective,
      audience: request.brief.audience,
      source_refs: [...request.brief.source_refs].sort(),
      tone: request.brief.tone,
      deadline: request.brief.deadline,
      approval_policy: request.brief.approval_policy,
      handoff_note: request.brief.handoff_note,
    },
  };
  return JSON.stringify(canonical);
}
