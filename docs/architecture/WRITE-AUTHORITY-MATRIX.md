# HiveCrew write-authority matrix

This matrix prevents HiveCrew from creating a second system of record.

| Domain object | Authoritative owner | HiveCrew may read | HiveCrew may write directly | Allowed HiveCrew action |
| --- | --- | --- | --- | --- |
| Employee identity | HiveCosm employee registry | Yes | No | Request registration or binding change |
| Department and position | HiveCosm organization registry | Yes | No | Submit organization-change command |
| Agent identity binding | HiveCosm identity-binding authority | Yes | No | Resolve exact binding; request reviewed change |
| Runtime profile | HiveCosm runtime registry | Yes | No | Select an admitted profile for a run |
| Model and API route | LiteLLM/model route authority | Yes | No secret values | Select route and record reference/digest |
| Harness and skills | HiveCosm harness/skill registries | Yes | No | Attach admitted version references |
| Conversation and chat session | HiveCrew | Yes | Yes | Create, send, archive and link context |
| Assignment and task run | HiveCrew | Yes | Yes | Create, schedule, execute, pause, retry, cancel |
| Execution receipt | HiveCrew append-only ledger | Yes | Append only | Record actor, binding, route, inputs, outputs and time |
| Temporary artifact | HiveCrew workspace | Yes | Yes | Create, revise and submit for review |
| Formal accepted artifact | HiveCosm delivery/project authority | Yes | No | Send explicit promotion command |
| Company project lifecycle | HiveCosm project registry | Yes | No | Link a WorkOrder or request lifecycle transition |
| Governance/policy/decision | HiveCosm governance authority | Yes | No | Read constraints and submit decision request |
| Knowledge graph/facts | HiveCosm knowledge authority | Yes | No | Propose fact or artifact promotion |
| QM sandbox/workspace | QM authority | Yes | Through QM contract only | Start or join governed joint-work session |

## Adapter rule

Every HiveCosm domain adapter must expose provenance: source URI, source revision,
content digest, observed time, freshness state and write capability. Missing or
ambiguous authority fails closed. A UI mutation is never evidence that company
truth changed; success requires a receipt from the authoritative write command.
