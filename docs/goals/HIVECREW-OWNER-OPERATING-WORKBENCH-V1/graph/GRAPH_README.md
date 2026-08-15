# Goal execution graph

- Scope: HiveCrew Owner Operating Workbench V1/v2, with Prime 0813 main-force W1-W4 formal convergence.
- Current source: `formal-convergence-v2.mmd`; the original `overview.mmd` remains the historical full-goal projection.
- Mermaid SHA-256: `034e64a1ebda8a5a38017f5da336eeed33649dfd71c191ee418b9e5e9e110924`.
- Generated at: `2026-08-14T10:54:25+08:00`.
- Outputs: `formal-convergence-v2/formal-convergence-v2.svg` and `formal-convergence-v2/formal-convergence-v2.png`.
- State label: `P2F_EXECUTING`; current execution state must be read from `../CHECKLIST.yaml`.

## Legend

- Blue: current truth and discovery.
- Purple: the minimum real operating loop.
- Green: management and scaling workstreams.
- Orange: candidate and release boundary.
- Dark: William's human-visible operating outcome.
- Solid arrows: hard dependency.
- Dashed arrows: mandatory review/evidence/journal loop, not a hard scheduling dependency.

## Truth boundary

This graph is a read-only projection. It cannot complete a Phase, change the ready frontier, or prove a VictoryCondition. `CHECKLIST.yaml` is the sole execution-state authority.

## Regeneration

```bash
/Users/jiawei/.codex/skills/mermaid-planning-graph/scripts/render_mermaid_plan_graph.sh \
  docs/goals/HIVECREW-OWNER-OPERATING-WORKBENCH-V1/graph/formal-convergence-v2.mmd \
  docs/goals/HIVECREW-OWNER-OPERATING-WORKBENCH-V1/graph/formal-convergence-v2
```
