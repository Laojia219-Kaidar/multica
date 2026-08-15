import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemorySection } from "./memory-section";
import type { MemoryCandidate } from "@multica/core/api/memory";

function c(over: Partial<MemoryCandidate> = {}): MemoryCandidate {
  return {
    id: "c1",
    employee_id: "EMP-01",
    kind: "episodic",
    content: "postgres queries tuned",
    evidence: [{ type: "run", id: "r1" }],
    source_refs: ["run://r1"],
    author_id: "EMP-01",
    created_at: "2026-08-13T12:00:00Z",
    status: "promoted",
    ...over,
  };
}

describe("MemorySection", () => {
  it("groups candidates by status with text labels", () => {
    render(
      <MemorySection
        candidates={[
          c(),
          c({ id: "c2", status: "validated", content: "draft note" }),
          c({ id: "c3", status: "revoked", content: "wrong conclusion" }),
        ]}
      />,
    );
    expect(screen.getByText(/已验证经验/)).toBeDefined();
    expect(screen.getByText(/经验候选/)).toBeDefined();
    expect(screen.getByText(/纠错与废弃/)).toBeDefined();
    expect(screen.getAllByText(/已验证/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/已撤销/).length).toBeGreaterThan(0);
  });

  it("shows evidence count", () => {
    render(<MemorySection candidates={[c()]} />);
    expect(screen.getByText(/证据 1/)).toBeDefined();
  });
});
