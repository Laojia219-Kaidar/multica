# Execution graph projection

`goal-overview.mmd` is a read-only visual projection of the authoritative
`../CHECKLIST.yaml`. It does not change Goal progress, authorise a merge or
deployment, or replace the worktree and verification receipts.

The diagram shows the only permitted join: three isolated implementation lanes
converge in `WO-40`, then the integrator performs a simulated-publish canary in
`WO-50`. Candidate Ready remains distinct from owner acceptance, production
deployment, real platform publishing, real trading, and storage writes.

Generated files are `goal-overview.svg` and `goal-overview.png`. Regenerate
them after changing the Mermaid source with:

```bash
/Users/jiawei/.codex/skills/mermaid-planning-graph/scripts/render_mermaid_plan_graph.sh \
  goal-overview.mmd .
```
