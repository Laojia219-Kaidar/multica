import { z } from "zod";

// Wire contract for the employee memory candidate layer
// (server/internal/memory). Strict wire: unknown keys are rejected.
// Secret detection and evidence validation happen server-side; this schema
// only pins the public DTO shape and closed enums.

export const MemoryKindSchema = z.enum(["episodic", "experience"]);
export const CandidateStatusSchema = z.enum(["pending", "validated", "rejected", "promoted", "revoked"]);
export const PromotionTargetSchema = z.enum(["employee_memory", "team_playbook", "skill"]);

export const EvidenceRefSchema = z.object({
  type: z.enum(["task", "run", "outcome"]),
  id: z.string(),
});

export const MemoryCandidateSchema = z
  .object({
    id: z.string(),
    employee_id: z.string(),
    position_id: z.string().optional(),
    kind: MemoryKindSchema,
    content: z.string(),
    evidence: z.array(EvidenceRefSchema).min(1),
    source_refs: z.array(z.string()),
    author_id: z.string(),
    created_at: z.string(),
    status: CandidateStatusSchema,
  })
  .strict();

export const MemoryPromotionSchema = z
  .object({
    candidate_id: z.string(),
    target: PromotionTargetSchema,
    reviewer_id: z.string(),
    approved: z.boolean(),
    reason: z.string(),
    promoted_at: z.string(),
  })
  .strict();

export type MemoryCandidate = z.infer<typeof MemoryCandidateSchema>;
export type MemoryPromotion = z.infer<typeof MemoryPromotionSchema>;
export type MemoryKind = z.infer<typeof MemoryKindSchema>;
export type CandidateStatus = z.infer<typeof CandidateStatusSchema>;
export type PromotionTarget = z.infer<typeof PromotionTargetSchema>;
