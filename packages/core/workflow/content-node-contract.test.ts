import { describe, expect, it } from "vitest";
import {
  WECHAT_CONTENT_APPROVAL_POLICIES,
  WECHAT_CONTENT_CHANNEL,
  WECHAT_CONTENT_CONTRACT_SCHEMA_VERSION,
  WECHAT_CONTENT_HANDOFF_NOTE_MAX_BYTES,
  WECHAT_CONTENT_LINEAGE_AUTHORITIES,
  WECHAT_CONTENT_NODE_KEYS,
  WECHAT_CONTENT_PRODUCTION_REQUEST_SCHEMA_VERSION,
  isValidRfc3339Datetime,
  validateWechatContentNodePlan,
  validateWechatContentProductionRequest,
  wechatContentCanonicalNodePlan,
  wechatContentNodeLineageKeys,
  wechatContentRequestIdempotencyFingerprint,
  type WechatContentContractIssue,
  type WechatContentContractValidationResult,
  type WechatContentProductionRequest,
} from "./content-node-contract";
import {
  WechatContentProductionContractSchema,
  WechatContentProductionRequestSchema,
} from "../api/workflow";

const SHA = "sha256:" + "a".repeat(64);
const AGENT_ID = "11111111-1111-4111-8111-111111111111";
const SESSION_ID = "22222222-2222-4222-8222-222222222222";
const WORK_ORDER =
  "hive://hivecosm/delivery/project/PRJ-WECHAT-OPS/work-order/WO-10";

function issuesOf<T>(
  result: WechatContentContractValidationResult<T>,
): WechatContentContractIssue[] {
  return result.ok ? [] : result.issues;
}

function okValue<T>(result: WechatContentContractValidationResult<T>): T {
  if (!result.ok) throw new Error(`expected ok result (${result.issues.length} issues)`);
  return result.value;
}

function validRequest(overrides: Record<string, unknown> = {}) {
  return {
    schema_version: WECHAT_CONTENT_PRODUCTION_REQUEST_SCHEMA_VERSION,
    channel: WECHAT_CONTENT_CHANNEL,
    project_id: "PRJ-WECHAT-OPS",
    authority: {
      work_order_source_ref: WORK_ORDER,
      employee_id: "EMP-001",
      identity_binding_id: "IB-001",
      agent_id: AGENT_ID,
      session_id: SESSION_ID,
    },
    definition: {
      definition_id: "content.wechat-production-package",
      version: 1,
      digest: SHA,
    },
    brief: {
      subject: "新品发布稿",
      objective: "向受众说明产品价值",
      audience: "公众号订阅用户",
      source_refs: ["ref://material/1"],
      tone: "专业",
      deadline: "2026-08-20T12:00:00Z",
      approval_policy: "owner_approval",
      handoff_note: "请根据资料包撰写一篇面向订阅用户的新品发布公众号文章。",
    },
    idempotency_key: "req-1",
    ...overrides,
  };
}

function frozenLineage() {
  return Object.fromEntries(
    Object.entries(WECHAT_CONTENT_LINEAGE_AUTHORITIES).map(([member, authority]) => [
      member,
      { required: true, authority },
    ]),
  );
}

function canonicalPlan() {
  return wechatContentCanonicalNodePlan().map((node) => ({
    key: node.key,
    order: node.order,
    required_upstream: node.requiredUpstream,
    artifact_kind: node.artifactKind,
    review_rule: node.reviewRule,
    lineage: frozenLineage(),
  }));
}

describe("frozen WeChat content node contract", () => {
  it("freezes exactly four nodes in prerequisite order", () => {
    expect(WECHAT_CONTENT_NODE_KEYS).toEqual([
      "research-material-package",
      "article-draft",
      "editorial-review-report",
      "wechat-publication-package",
    ]);
    const plan = wechatContentCanonicalNodePlan();
    expect(plan).toHaveLength(4);
    expect(plan.map((n) => n.key)).toEqual(WECHAT_CONTENT_NODE_KEYS);
    expect(plan.map((n) => n.requiredUpstream)).toEqual([
      null,
      "research-material-package",
      "article-draft",
      "editorial-review-report",
    ]);
    expect(plan.map((n) => n.artifactKind)).toEqual([
      "wechat.research-material-package.v1",
      "wechat.article-draft.v1",
      "wechat.editorial-review-report.v1",
      "wechat.wechat-publication-package.v1",
    ]);
    expect(plan.map((n) => n.reviewRule)).toEqual([
      "auto_accept",
      "editorial_review",
      "approval_gate",
      "owner_approval",
    ]);
  });

  it("freezes the six-member lineage authorities as contract constants", () => {
    const members = [
      "issue",
      "assignment",
      "task",
      "run",
      "candidate",
      "outcome",
    ] as const;
    for (const node of wechatContentCanonicalNodePlan()) {
      for (const member of members) {
        expect(node.lineage[member].required).toBe(true);
        expect(node.lineage[member].authority).toBe(
          WECHAT_CONTENT_LINEAGE_AUTHORITIES[member],
        );
      }
    }
  });

  it("gives every node its own durable lineage namespace", () => {
    const seen = new Set<string>();
    for (const key of WECHAT_CONTENT_NODE_KEYS) {
      const lineage = wechatContentNodeLineageKeys(key);
      for (const member of Object.values(lineage)) {
        expect(seen.has(member)).toBe(false);
        seen.add(member);
      }
    }
    expect(seen.size).toBe(24);
  });
});

describe("isValidRfc3339Datetime", () => {
  it("accepts Z and numeric offsets, rejects invalid and timezone-less values", () => {
    expect(isValidRfc3339Datetime("2026-08-20T12:00:00Z")).toBe(true);
    expect(isValidRfc3339Datetime("2026-08-20T20:00:00+08:00")).toBe(true);
    expect(isValidRfc3339Datetime("2026-08-20T04:00:00-08:00")).toBe(true);
    expect(isValidRfc3339Datetime("2026-08-20T12:00:00.123456789Z")).toBe(true);
    expect(isValidRfc3339Datetime("2026-13-40T99:99:99Z")).toBe(false);
    expect(isValidRfc3339Datetime("2026-02-30T12:00:00Z")).toBe(false);
    expect(isValidRfc3339Datetime("2026-08-20T12:00:00")).toBe(false);
    expect(isValidRfc3339Datetime("2026-08-20T12:00:00+24:00")).toBe(false);
    expect(isValidRfc3339Datetime("2026-08-20T12:00:00+08:60")).toBe(false);
    expect(isValidRfc3339Datetime("2026-08-20T12:00:00+23:59")).toBe(true);
    expect(isValidRfc3339Datetime("tomorrow")).toBe(false);
  });
});

describe("validateWechatContentProductionRequest", () => {
  it("accepts a complete, well-formed request", () => {
    const result = validateWechatContentProductionRequest(validRequest());
    expect(result.ok).toBe(true);
  });

  it("fails closed on unsupported schema version and channel", () => {
    const versionResult = validateWechatContentProductionRequest(
      validRequest({ schema_version: "hivecrew.wechat-content-production-request.v99" }),
    );
    expect(versionResult.ok).toBe(false);
    expect(issuesOf(versionResult).map((i) => i.code)).toContain(
      "unsupported_schema_version",
    );

    const channelResult = validateWechatContentProductionRequest(
      validRequest({ channel: "xiaohongshu" }),
    );
    expect(channelResult.ok).toBe(false);
    expect(issuesOf(channelResult).map((i) => i.code)).toContain(
      "unsupported_channel",
    );
  });

  it("fails closed on absent or malformed authority fields", () => {
    const absent = validRequest();
    delete (absent as Record<string, unknown>).authority;
    const absentResult = validateWechatContentProductionRequest(absent);
    expect(absentResult.ok).toBe(false);
    expect(issuesOf(absentResult).map((i) => i.code)).toContain(
      "missing_authority_context",
    );

    const badWorkOrder = validRequest({
      authority: { ...validRequest().authority, work_order_source_ref: "not-a-hive-ref" },
    });
    const badWorkOrderResult = validateWechatContentProductionRequest(badWorkOrder);
    expect(badWorkOrderResult.ok).toBe(false);
    expect(issuesOf(badWorkOrderResult).map((i) => i.code)).toContain(
      "invalid_authority_field",
    );

    const badAgent = validRequest({
      authority: { ...validRequest().authority, agent_id: "not-a-uuid" },
    });
    const badAgentResult = validateWechatContentProductionRequest(badAgent);
    expect(badAgentResult.ok).toBe(false);
    expect(issuesOf(badAgentResult).map((i) => i.code)).toContain(
      "invalid_authority_field",
    );
  });

  it("fails closed on cross-project authority", () => {
    const cross = validRequest({ project_id: "PRJ-OTHER" });
    const result = validateWechatContentProductionRequest(cross);
    expect(result.ok).toBe(false);
    expect(issuesOf(result).map((i) => i.code)).toContain(
      "cross_project_authority_mismatch",
    );
  });

  it("fails closed on missing or invalid definition version binding", () => {
    const missing = validRequest();
    delete (missing as Record<string, unknown>).definition;
    const missingResult = validateWechatContentProductionRequest(missing);
    expect(missingResult.ok).toBe(false);
    expect(issuesOf(missingResult).map((i) => i.code)).toContain(
      "missing_definition_binding",
    );

    const zeroVersion = validateWechatContentProductionRequest(
      validRequest({ definition: { ...validRequest().definition, version: 0 } }),
    );
    expect(zeroVersion.ok).toBe(false);
    expect(issuesOf(zeroVersion).map((i) => i.code)).toContain(
      "invalid_definition_binding",
    );

    const badDigest = validateWechatContentProductionRequest(
      validRequest({ definition: { ...validRequest().definition, digest: "not-sha256" } }),
    );
    expect(badDigest.ok).toBe(false);
    expect(issuesOf(badDigest).map((i) => i.code)).toContain(
      "invalid_definition_binding",
    );
  });

  it("fails closed on missing or invalid brief fields", () => {
    const missing = validRequest();
    delete (missing as Record<string, unknown>).brief;
    const missingResult = validateWechatContentProductionRequest(missing);
    expect(missingResult.ok).toBe(false);
    expect(issuesOf(missingResult).map((i) => i.code)).toContain("missing_brief");

    const badSourceRefs = validateWechatContentProductionRequest(
      validRequest({ brief: { ...validRequest().brief, source_refs: [""] } }),
    );
    expect(badSourceRefs.ok).toBe(false);
    expect(issuesOf(badSourceRefs).map((i) => i.code)).toContain(
      "invalid_brief_field",
    );

    const badPolicy = validateWechatContentProductionRequest(
      validRequest({ brief: { ...validRequest().brief, approval_policy: "whatever" } }),
    );
    expect(badPolicy.ok).toBe(false);
    expect(issuesOf(badPolicy).map((i) => i.code)).toContain("invalid_brief_field");
  });

  it("fails closed on an empty source_refs array", () => {
    const result = validateWechatContentProductionRequest(
      validRequest({ brief: { ...validRequest().brief, source_refs: [] } }),
    );
    expect(result.ok).toBe(false);
    expect(issuesOf(result).map((i) => i.code)).toContain("invalid_brief_field");
  });

  it("requires a handoff note (trimmed non-empty, max 32 KiB)", () => {
    const missingBrief = validRequest().brief as Record<string, unknown>;
    delete missingBrief.handoff_note;
    const missing = validateWechatContentProductionRequest(
      validRequest({ brief: missingBrief }),
    );
    expect(missing.ok).toBe(false);
    expect(issuesOf(missing).map((i) => i.code)).toContain("invalid_brief_field");

    const blank = validateWechatContentProductionRequest(
      validRequest({ brief: { ...validRequest().brief, handoff_note: "  \n\t " } }),
    );
    expect(blank.ok).toBe(false);

    const oversize = validateWechatContentProductionRequest(
      validRequest({
        brief: {
          ...validRequest().brief,
          handoff_note: "x".repeat(WECHAT_CONTENT_HANDOFF_NOTE_MAX_BYTES + 1),
        },
      }),
    );
    expect(oversize.ok).toBe(false);

    const exact = validateWechatContentProductionRequest(
      validRequest({
        brief: {
          ...validRequest().brief,
          handoff_note: "x".repeat(WECHAT_CONTENT_HANDOFF_NOTE_MAX_BYTES),
        },
      }),
    );
    expect(exact.ok).toBe(true);
  });

  it("accepts Z and offset deadlines, rejects invalid and timezone-less deadlines", () => {
    for (const deadline of [
      "2026-08-20T12:00:00Z",
      "2026-08-20T20:00:00+08:00",
      "2026-08-20T04:00:00-08:00",
    ]) {
      const result = validateWechatContentProductionRequest(
        validRequest({ brief: { ...validRequest().brief, deadline } }),
      );
      expect(result.ok).toBe(true);
    }
    for (const deadline of [
      "2026-13-40T99:99:99Z",
      "2026-02-30T12:00:00Z",
      "2026-08-20T12:00:00",
      "2026-08-20T12:00:00+24:00",
      "2026-08-20T12:00:00+08:60",
      "tomorrow",
    ]) {
      const result = validateWechatContentProductionRequest(
        validRequest({ brief: { ...validRequest().brief, deadline } }),
      );
      expect(result.ok).toBe(false);
      expect(issuesOf(result).map((i) => i.code)).toContain("invalid_brief_field");
    }
  });

  it("fails closed on missing idempotency key", () => {
    const missing = validRequest();
    delete (missing as Record<string, unknown>).idempotency_key;
    const result = validateWechatContentProductionRequest(missing);
    expect(result.ok).toBe(false);
    expect(issuesOf(result).map((i) => i.code)).toContain("missing_idempotency_key");
  });

  it("fails closed on caller-supplied execution/artifact/outcome proof", () => {
    for (const proofKey of [
      "task_id",
      "run_id",
      "initial_task_id",
      "current_task_id",
      "execution_receipt",
      "execution_state",
      "candidate_id",
      "artifact_id",
      "formal_artifact_ref",
      "outcome_id",
      "input_digest",
    ]) {
      const result = validateWechatContentProductionRequest(
        validRequest({ [proofKey]: "forged-proof" }),
      );
      expect(result.ok).toBe(false);
      expect(issuesOf(result).map((i) => i.code)).toContain(
        "caller_supplied_execution_proof",
      );
    }
  });

  it("fails closed on nested forged proof at any depth", () => {
    const nestedInArray = validRequest({
      brief: {
        ...validRequest().brief,
        source_refs: ["ref://material/1", { task_id: "forged" }],
      },
    });
    const arrayResult = validateWechatContentProductionRequest(nestedInArray);
    expect(arrayResult.ok).toBe(false);
    expect(issuesOf(arrayResult).map((i) => i.code)).toContain(
      "caller_supplied_execution_proof",
    );

    const nestedInAuthority = validRequest({
      authority: {
        ...validRequest().authority,
        extra: { deep: { execution_receipt: { state: "completed" } } },
      },
    });
    const authorityResult = validateWechatContentProductionRequest(nestedInAuthority);
    expect(authorityResult.ok).toBe(false);
    const codes = issuesOf(authorityResult).map((i) => i.code);
    expect(codes).toContain("caller_supplied_execution_proof");
    expect(codes).toContain("unknown_field");

    const clientDigest = validRequest({
      authority: { ...validRequest().authority, input_digest: SHA },
    });
    const digestResult = validateWechatContentProductionRequest(clientDigest);
    expect(digestResult.ok).toBe(false);
    expect(issuesOf(digestResult).map((i) => i.code)).toContain(
      "caller_supplied_execution_proof",
    );
  });

  it("fails closed on unknown fields (top level and nested sections)", () => {
    const result = validateWechatContentProductionRequest(validRequest({ surprise: true }));
    expect(result.ok).toBe(false);
    expect(issuesOf(result).map((i) => i.code)).toContain("unknown_field");

    const nestedBrief = validateWechatContentProductionRequest(
      validRequest({ brief: { ...validRequest().brief, nested: { x: 1 } } }),
    );
    expect(nestedBrief.ok).toBe(false);
    expect(issuesOf(nestedBrief).map((i) => i.code)).toContain("unknown_field");

    const nestedDefinition = validateWechatContentProductionRequest(
      validRequest({ definition: { ...validRequest().definition, extra: "x" } }),
    );
    expect(nestedDefinition.ok).toBe(false);
    expect(issuesOf(nestedDefinition).map((i) => i.code)).toContain("unknown_field");
  });
});

describe("validateWechatContentNodePlan", () => {
  it("accepts the canonical four-node plan", () => {
    const result = validateWechatContentNodePlan(
      canonicalPlan().map(({ lineage: _lineage, ...entry }) => entry),
    );
    expect(result.ok).toBe(true);
    expect(okValue(result)).toHaveLength(4);
  });

  it("accepts the canonical plan with the frozen lineage shape", () => {
    const result = validateWechatContentNodePlan(canonicalPlan());
    expect(result.ok).toBe(true);
  });

  it("fails closed on empty and unknown-node plans", () => {
    const empty = validateWechatContentNodePlan([]);
    expect(empty.ok).toBe(false);
    expect(issuesOf(empty).map((i) => i.code)).toContain("empty_node_plan");

    const unknown = validateWechatContentNodePlan([{ key: "not-a-node" }]);
    expect(unknown.ok).toBe(false);
    expect(issuesOf(unknown).map((i) => i.code)).toContain("unknown_node");
  });

  it("fails closed on duplicate and missing nodes", () => {
    const entries = canonicalPlan().map(({ lineage: _l, ...entry }) => entry);
    const duplicated = [entries[0], { ...entries[0], order: 1 }, entries[1], entries[2]];
    const dupResult = validateWechatContentNodePlan(duplicated);
    expect(dupResult.ok).toBe(false);
    expect(issuesOf(dupResult).map((i) => i.code)).toContain("duplicate_node");

    const missing = validateWechatContentNodePlan([entries[0], entries[1], entries[2]]);
    expect(missing.ok).toBe(false);
    expect(issuesOf(missing).map((i) => i.code)).toContain("missing_node");
  });

  it("fails closed on altered nodes", () => {
    const entries = canonicalPlan().map(({ lineage: _l, ...entry }) => entry);
    const altered = entries.map((entry, index) =>
      index === 0 ? { ...entry, artifact_kind: "wechat.changed.v1" } : entry,
    );
    const result = validateWechatContentNodePlan(altered);
    expect(result.ok).toBe(false);
    expect(issuesOf(result).map((i) => i.code)).toContain("altered_node");
  });

  it("fails closed on non-frozen lineage authority metadata", () => {
    const wrongAuthority = canonicalPlan().map((entry, index) =>
      index === 0
        ? {
            ...entry,
            lineage: { ...frozenLineage(), task: { required: true, authority: "caller-chosen" } },
          }
        : entry,
    );
    const authorityResult = validateWechatContentNodePlan(wrongAuthority);
    expect(authorityResult.ok).toBe(false);
    expect(issuesOf(authorityResult).map((i) => i.code)).toContain("altered_node");

    const missingMember = canonicalPlan().map((entry, index) => {
      if (index !== 0) return entry;
      const lineage = frozenLineage() as Record<string, unknown>;
      delete lineage.outcome;
      return { ...entry, lineage };
    });
    const memberResult = validateWechatContentNodePlan(missingMember);
    expect(memberResult.ok).toBe(false);
    expect(issuesOf(memberResult).map((i) => i.code)).toContain("altered_node");

    const extraMember = canonicalPlan().map((entry, index) =>
      index === 0
        ? { ...entry, lineage: { ...frozenLineage(), seventh: { required: true, authority: "x" } } }
        : entry,
    );
    const extraResult = validateWechatContentNodePlan(extraMember);
    expect(extraResult.ok).toBe(false);
    expect(issuesOf(extraResult).map((i) => i.code)).toContain("unknown_field");
  });

  it("fails closed on broken prerequisites (wrong order)", () => {
    const entries = canonicalPlan().map(({ lineage: _l, ...entry }) => entry);
    const reordered = [entries[1], entries[0], entries[2], entries[3]];
    const result = validateWechatContentNodePlan(reordered);
    expect(result.ok).toBe(false);
    expect(issuesOf(result).map((i) => i.code)).toContain("broken_prerequisite");
  });

  it("fails closed on caller-supplied proof inside a node entry", () => {
    const entries = canonicalPlan().map(({ lineage: _l, ...entry }) => entry);
    const poisoned = entries.map((entry, index) =>
      index === 0 ? { ...entry, run_id: "forged" } : entry,
    );
    const result = validateWechatContentNodePlan(poisoned);
    expect(result.ok).toBe(false);
    expect(issuesOf(result).map((i) => i.code)).toContain(
      "caller_supplied_execution_proof",
    );

    const nested = entries.map((entry, index) =>
      index === 0
        ? { ...entry, lineage: { ...frozenLineage(), task: { required: true, authority: WECHAT_CONTENT_LINEAGE_AUTHORITIES.task, input_digest: SHA } } }
        : entry,
    );
    const nestedResult = validateWechatContentNodePlan(nested);
    expect(nestedResult.ok).toBe(false);
    expect(issuesOf(nestedResult).map((i) => i.code)).toContain(
      "caller_supplied_execution_proof",
    );
  });
});

describe("idempotency semantics", () => {
  it("produces an identical fingerprint for an identical request", () => {
    const a = validRequest() as unknown as WechatContentProductionRequest;
    const b = validRequest() as unknown as WechatContentProductionRequest;
    expect(wechatContentRequestIdempotencyFingerprint(a)).toBe(
      wechatContentRequestIdempotencyFingerprint(b),
    );
  });

  it("produces a different fingerprint when the brief changes", () => {
    const a = validRequest() as unknown as WechatContentProductionRequest;
    const b = validRequest({
      brief: { ...validRequest().brief, subject: "改过的主题" },
    }) as unknown as WechatContentProductionRequest;
    expect(wechatContentRequestIdempotencyFingerprint(a)).not.toBe(
      wechatContentRequestIdempotencyFingerprint(b),
    );
  });

  it("produces a different fingerprint when the handoff note changes", () => {
    const a = validRequest() as unknown as WechatContentProductionRequest;
    const b = validRequest({
      brief: { ...validRequest().brief, handoff_note: "改过的 handoff" },
    }) as unknown as WechatContentProductionRequest;
    expect(wechatContentRequestIdempotencyFingerprint(a)).not.toBe(
      wechatContentRequestIdempotencyFingerprint(b),
    );
  });
});

describe("WeChat content wire contract (zod)", () => {
  it("parses a valid request and rejects unknown keys", () => {
    expect(WechatContentProductionRequestSchema.parse(validRequest()).channel).toBe(
      "wechat",
    );
    expect(() =>
      WechatContentProductionRequestSchema.parse(validRequest({ extra: true })),
    ).toThrow();
  });

  it("rejects caller-supplied proof and cross-project authority", () => {
    expect(
      WechatContentProductionRequestSchema.safeParse(validRequest({ task_id: "forged" }))
        .success,
    ).toBe(false);
    expect(
      WechatContentProductionRequestSchema.safeParse(validRequest({ input_digest: SHA }))
        .success,
    ).toBe(false);
    expect(
      WechatContentProductionRequestSchema.safeParse(validRequest({ project_id: "PRJ-OTHER" }))
        .success,
    ).toBe(false);
  });

  it("requires a handoff note and enforces the 32 KiB cap", () => {
    const brief = validRequest().brief as Record<string, unknown>;
    delete brief.handoff_note;
    expect(
      WechatContentProductionRequestSchema.safeParse(validRequest({ brief })).success,
    ).toBe(false);
    expect(
      WechatContentProductionRequestSchema.safeParse(
        validRequest({ brief: { ...validRequest().brief, handoff_note: "   " } }),
      ).success,
    ).toBe(false);
    expect(
      WechatContentProductionRequestSchema.safeParse(
        validRequest({
          brief: {
            ...validRequest().brief,
            handoff_note: "x".repeat(WECHAT_CONTENT_HANDOFF_NOTE_MAX_BYTES + 1),
          },
        }),
      ).success,
    ).toBe(false);
    expect(
      WechatContentProductionRequestSchema.safeParse(
        validRequest({
          brief: {
            ...validRequest().brief,
            handoff_note: "x".repeat(WECHAT_CONTENT_HANDOFF_NOTE_MAX_BYTES),
          },
        }),
      ).success,
    ).toBe(true);
  });

  it("accepts Z and offset deadlines and rejects invalid ones", () => {
    for (const deadline of ["2026-08-20T12:00:00Z", "2026-08-20T20:00:00+08:00"]) {
      expect(
        WechatContentProductionRequestSchema.safeParse(
          validRequest({ brief: { ...validRequest().brief, deadline } }),
        ).success,
      ).toBe(true);
    }
    for (const deadline of [
      "2026-13-40T99:99:99Z",
      "2026-02-30T12:00:00Z",
      "2026-08-20T12:00:00",
      "2026-08-20T12:00:00+24:00",
      "2026-08-20T12:00:00+08:60",
    ]) {
      expect(
        WechatContentProductionRequestSchema.safeParse(
          validRequest({ brief: { ...validRequest().brief, deadline } }),
        ).success,
      ).toBe(false);
    }
  });

  it("requires at least one source ref", () => {
    expect(
      WechatContentProductionRequestSchema.safeParse(
        validRequest({ brief: { ...validRequest().brief, source_refs: [] } }),
      ).success,
    ).toBe(false);
  });

  it("parses the frozen production contract and rejects altered nodes", () => {
    const contract = {
      schema_version: WECHAT_CONTENT_CONTRACT_SCHEMA_VERSION,
      channel: WECHAT_CONTENT_CHANNEL,
      nodes: canonicalPlan(),
    };
    expect(WechatContentProductionContractSchema.parse(contract).nodes).toHaveLength(4);

    const altered = {
      ...contract,
      nodes: canonicalPlan().map((node, index) =>
        index === 0 ? { ...node, artifact_kind: "wechat.changed.v1" } : node,
      ),
    };
    expect(WechatContentProductionContractSchema.safeParse(altered).success).toBe(false);

    const wrongLineage = {
      ...contract,
      nodes: canonicalPlan().map((node, index) =>
        index === 0
          ? {
              ...node,
              lineage: { ...frozenLineage(), task: { required: true, authority: "caller-chosen" } },
            }
          : node,
      ),
    };
    expect(WechatContentProductionContractSchema.safeParse(wrongLineage).success).toBe(false);
  });
});

describe("contract metadata", () => {
  it("exposes the frozen contract version and channel", () => {
    expect(WECHAT_CONTENT_CONTRACT_SCHEMA_VERSION).toBe(
      "hivecrew.wechat-content-node-contract.v1",
    );
    expect(WECHAT_CONTENT_CHANNEL).toBe("wechat");
    expect(WECHAT_CONTENT_APPROVAL_POLICIES).toContain("owner_approval");
  });
});
