import { describe, expect, it } from "vitest";
import { MemoryCandidateSchema, MemoryPromotionSchema } from "./memory";

const validCandidate = {
  id: "c1",
  employee_id: "EMP-01",
  kind: "episodic",
  content: "run completed with 32 passing tests",
  evidence: [{ type: "run", id: "r1" }],
  source_refs: ["run://r1"],
  author_id: "EMP-01",
  created_at: "2026-08-13T12:00:00Z",
  status: "validated",
};

describe("MemoryCandidateSchema", () => {
  it("accepts a valid candidate", () => {
    expect(MemoryCandidateSchema.parse(validCandidate).id).toBe("c1");
  });

  it("rejects unknown keys (strict wire)", () => {
    expect(() => MemoryCandidateSchema.parse({ ...validCandidate, raw_thought: "x" })).toThrow();
  });

  it("rejects empty evidence (must bind Task/Run/Outcome)", () => {
    expect(() => MemoryCandidateSchema.parse({ ...validCandidate, evidence: [] })).toThrow();
  });

  it("rejects invalid kind", () => {
    expect(() => MemoryCandidateSchema.parse({ ...validCandidate, kind: "telepathy" })).toThrow();
  });

  it("rejects invalid evidence type", () => {
    expect(() =>
      MemoryCandidateSchema.parse({ ...validCandidate, evidence: [{ type: "chat", id: "x" }] }),
    ).toThrow();
  });
});

describe("MemoryPromotionSchema", () => {
  it("accepts a promotion receipt", () => {
    const p = {
      candidate_id: "c1",
      target: "employee_memory",
      reviewer_id: "REV-01",
      approved: true,
      reason: "verified",
      promoted_at: "2026-08-13T12:00:00Z",
    };
    expect(MemoryPromotionSchema.parse(p).reviewer_id).toBe("REV-01");
  });

  it("rejects unknown target", () => {
    const p = {
      candidate_id: "c1",
      target: "company_secret",
      reviewer_id: "REV-01",
      approved: true,
      reason: "",
      promoted_at: "2026-08-13T12:00:00Z",
    };
    expect(() => MemoryPromotionSchema.parse(p)).toThrow();
  });
});
