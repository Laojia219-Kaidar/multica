# Phase 1 operating-loop contract

Status: candidate contract for P1 review  
Goal: `HIVECREW-OWNER-OPERATING-WORKBENCH-V1`  
Baseline: `43d7c95dfbf50e3b53a328b2c35a6bd36a5ddf1d`  
Authority: this document defines the P2 implementation boundary; it does not claim that the missing objects or writers already exist.

## 1. One operator journey

> William opens the existing HiveCrew Chat or Inbox, selects one exact HiveCosm Employee through a current, conflict-free IdentityBinding to one callable HiveCrew Agent, creates or opens one authoritative WorkOrder, confirms an Assignment, observes one real Run and append-only Execution Receipt, reviews its Temporary Artifact, requests revision or accepts it, and sees an authoritative Promotion receipt plus the resulting Formal Artifact reference without any local UI state pretending to be company truth.

The first pilot is intentionally one vertical slice. It does not add a new top-level navigation system, does not rename Issue into WorkOrder, does not treat Agent as Employee, and does not attempt a complete Organization, Outcome Center, QM, or distributed-scheduler implementation.

## 2. Current truth versus missing contract

### 2.1 Existing real paths that may be reused

1. **Direct conversation and Run**
   - Agent Detail opens Chat with the exact local `agent.id`: `packages/views/agents/components/agent-detail-page.tsx:261`.
   - Chat accepts `session=<chat_session_id>` and `agent=<agent_id>`: `packages/views/chat/chat-page.tsx:31`.
   - Chat lazily creates a session and sends a message through the real mutations: `packages/views/chat/components/use-chat-controller.ts:369`, `:482`; `packages/core/chat/mutations.ts:99`.
   - The send response already returns a real `message_id`, `task_id`, and `created_at`; that `task_id` is the current Run identifier.

2. **Issue assignment and daemon execution**
   - Issue assignment UI uses real Agent/Squad IDs: `packages/views/issues/components/pickers/assignee-picker.tsx:186`.
   - The current confirmation updates an Issue: `packages/views/modals/run-confirm.tsx:143`.
   - The backend detects assignment/status changes and can enqueue a task: `server/internal/handler/issue.go:2889`, `:2964`; `server/internal/service/issue_trigger.go:64`.
   - `TaskService.EnqueueTaskForIssueWithHandoff` and `agent_task_queue` form the existing Run pipeline: `server/internal/service/task.go:926`; `server/pkg/db/queries/agent.sql:248`.
   - Daemon claim, start, provider execution, completion and failure are real: `server/internal/handler/daemon.go:1387`, `:2930`, `:3590`; `server/internal/daemon/daemon.go:4965`, `:5146`, `:5762`.

3. **Run activity and temporary output carriers**
   - `ExecutionLogSection`, `ActiveTaskRow`, transcript and cancel/retry UI already expose current Run state: `packages/views/issues/components/execution-log-section.tsx:60`.
   - Comments have a canonical create path and preserve `source_task_id`: `server/internal/handler/comment.go:1245`, `:1427`; `server/pkg/db/queries/comment.sql:395`.
   - Attachments can be linked to Issue, Comment, Chat Message, Session and Task: `server/pkg/db/queries/attachment.sql:1`, `:70`, `:113`.
   - Terminal task rows already contain result/error, attempt lineage, session/workdir and attribution: `server/internal/handler/agent.go:273`, `:401`.

4. **Reusable workspace shells**
   - Inbox already has a resizable list/detail shell and stable Issue deep link: `packages/views/inbox/components/inbox-page.tsx:67`, `:106`, `:430`.
   - Chat already has a conversation list, conversation pane, shared composer and target-aware failure retention: `packages/views/chat/chat-page.tsx:31`; `packages/views/chat/components/use-chat-controller.ts:196`.
   - Issue Surface already supplies list/board/detail interaction: `packages/views/issues/surface/issue-surface.tsx:51`.

### 2.2 Missing facts that must not be simulated

- HiveCrew has no authoritative WorkOrder entity or WorkOrder command receipt.
- HiveCrew Agent records do not prove HiveCosm Employee identity or Appointment.
- Current Issue assignment is not a first-class Assignment receipt and the update response does not prove which Run was created.
- `agent_task_queue`, usage rows and task messages are mutable operational records, not one immutable Execution Receipt.
- Attachments and comments are temporary execution artifacts; there is no ArtifactCandidate, Review or Promotion writer.
- Attachment rows have no content digest, `task_id` is explicitly a temporary handle, and Issue/Comment deletion can cascade to attachment rows and stored objects; therefore an accepted candidate must not depend on attachment lifetime: `server/migrations/164_attachment_task_id.up.sql:1`, `server/internal/handler/issue.go:3160`, `server/internal/handler/comment.go:2857`.
- HiveCrew local Project CRUD is not HiveCosm Company Project lifecycle authority.
- The landing-page command center is static product demonstration content and cannot be used as acceptance evidence: `apps/web/features/landing/components/landing-hero.tsx:125`.

## 3. Page conversion canvas for the first slice

| Canvas field | Frozen P2 choice |
| --- | --- |
| Operator goal | Talk to one exact Employee, confirm one WorkOrder assignment, inspect the real Run, review the result and explicitly promote an accepted result. |
| Discoverable entry | Existing `/{workspaceSlug}/chat`; later Inbox attention items may deep-link into the same context. No new L1 navigation in P2. |
| Stable URL | `/{workspaceSlug}/chat?employee_id=<employee_id>&identity_binding_id=<identity_binding_id>&agent_id=<agent_id>&work_order_source_ref=<work_order_source_ref>&session_id=<chat_session_id>`; every supplied ID must resolve exactly or the action fails closed. |
| Context list | Existing Chat session list. It remains a HiveCrew interaction list, not an Employee roster. |
| Main content | Existing conversation plus a source-backed WorkOrder context card, Assignment confirmation, Run/Receipt timeline, Temporary Artifact preview and Review/Promotion controls. |
| Primary action | `确认派工` only after WorkOrder and exact EmployeeBinding have current authority evidence. |
| Secondary actions | Send message, cancel/retry eligible Run, submit feedback, request revision, accept candidate, request Promotion. |
| Receipt area | Inline IDs and state: WorkOrder source ref/revision, Employee ID, Binding ID/digest, Agent ID, Assignment ID, Run ID, Execution Receipt ID, ArtifactCandidate ID, Promotion command receipt and Formal Artifact ref. |
| Error retention | Conversation draft, WorkOrder draft, exact target tuple and feedback draft remain visible after any failed writer. |
| Refetch | Exact WorkOrder projection, binding snapshot, chat session, Run, Execution Receipt, ArtifactCandidate/Review and authority Promotion readback. |
| Rollback | Remove/disable the P2 adapter and UI card; existing Chat, Issue, Agent, daemon and attachment paths remain unchanged. Schema changes must have down migrations and no Company truth deletion. |

## 4. Truth map

| Visible object | Read source | Write owner | HiveCrew direct write? | Success proof |
| --- | --- | --- | --- | --- |
| Employee | HiveCosm Employee registry adapter | HiveCosm Employee authority | No | Source URI, revision, digest, observed time and current state |
| IdentityBinding | HiveCosm identity-binding adapter | HiveCosm identity authority | No | Exact active Employee ID ↔ HiveCrew Agent UUID binding, revision and digest |
| Agent | Existing HiveCrew Agent API/database | HiveCrew | Yes, existing writer only | Exact Agent UUID readback |
| Conversation/Session | Existing HiveCrew Chat API/database | HiveCrew | Yes | Session/message IDs and exact refetch |
| Company Project | HiveCosm Project adapter | HiveCosm Project authority | No | Source revision/digest; any lifecycle mutation needs authority receipt |
| WorkOrder | HiveCosm WorkOrder adapter | HiveCosm WorkOrder authority | No | WorkOrder source ref, revision/digest and authority command receipt |
| Local execution projection | HiveCrew Issue read model | HiveCrew | Yes, through `IssueService.Create` | Local Issue ID linked to exact external source, never treated as WorkOrder truth |
| Assignment | HiveCrew Assignment service | HiveCrew | Yes | Assignment ID, exact target tuple and created-at receipt |
| Run | `agent_task_queue` and daemon read model | HiveCrew | Yes | Run/task ID and state transition readback |
| Execution Receipt | New HiveCrew append-only ledger | HiveCrew | Append only | Receipt ID and immutable input/route/output snapshot |
| Temporary Artifact | New HiveCrew ArtifactCandidate over attachment/comment/task output | HiveCrew | Yes | Candidate ID, content digest, storage ref, Run/Receipt lineage |
| Review | New HiveCrew review ledger | HiveCrew | Append only | Review ID, actor, decision, feedback and target candidate revision |
| Formal Artifact | HiveCosm delivery/project adapter | HiveCosm Formal Artifact authority | No | Promotion command receipt and authoritative Formal Artifact ref readback |

The permanent authority matrix remains `docs/architecture/WRITE-AUTHORITY-MATRIX.md`. If a connector cannot provide provenance or returns ambiguous identity, `write_capability` is false.

## 5. Minimum anti-corruption contracts

### 5.1 Authority snapshot envelope

Every HiveCosm read adapter returns:

```ts
type AuthoritySnapshot<T> = {
  source_ref: string
  source_revision: string
  content_sha256: string
  observed_at: string
  freshness: "current" | "stale" | "unknown"
  write_capability: false | "command_only"
  value: T
}
```

Missing ref/revision/digest, stale data outside the configured bound, duplicate active records, schema mismatch or an unrecognised source fails closed. The P2 implementation may use a dependency-injected fixture only in tests. A browser pilot must use the observed authority source and display its provenance.

### 5.2 WorkOrder execution link, not a WorkOrder mirror

HiveCrew may add one narrow mapping record:

```text
workspace_id
source_ref
source_revision
content_sha256
observed_at
freshness
local_issue_id
created_at
UNIQUE(workspace_id, source_ref)
```

It must not copy authoritative WorkOrder status, acceptance, Owner, policy, or Project lifecycle. Command idempotency is:

```text
UNIQUE(workspace_id, source_ref, source_revision_or_command_id, operation)
```

Replaying the same command returns the same local Issue/Assignment/Run identifiers.

### 5.3 Exact Employee binding

The compose target is an immutable tuple captured before dispatch:

```ts
type ExecutionTarget = {
  employee_id: string
  identity_binding_id: string
  binding_revision: string
  binding_sha256: string
  agent_id: string
}
```

The complete active binding authority must be validated for both Employee-ID uniqueness and Agent-UUID uniqueness before projecting the selected target. The mandatory order is `Employee -> active IdentityBinding -> Agent UUID -> Assignment -> Run`; Assignment creation is impossible before the binding snapshot is valid. Display name, model name, array order, first Agent, first Session or Squad role are never identity. Agent is an execution carrier; Squad is a collaboration unit; neither is Department or Employee.

### 5.4 Assignment and Run receipt

The current `UpdateIssue` handler contains usable validation/dispatch logic but directly invokes the query path. P2 must extract or wrap it with one narrow `AssignmentService`; a new adapter must not call raw `UpdateIssue` SQL.

The assignment command returns atomically:

```ts
type AssignmentDispatchReceipt = {
  assignment_id: string
  local_issue_id: string
  run_id: string
  work_order_source_ref: string
  work_order_revision: string
  employee_id: string
  identity_binding_id: string
  agent_id: string
  created_at: string
}
```

At Assignment creation, the caller also freezes one `ExecutionTargetSnapshot` containing the complete target tuple plus `work_order_source_ref`, `work_order_revision`, `work_order_content_sha256`, `work_order_observed_at` and `input_digest`. Later WorkOrder revisions may be displayed as newer authority facts but must never rewrite the Run's frozen target or receipt.

The Run uses the current task queue and daemon pipeline. The new append-only Execution Receipt is unique by `run_id`; claim-time fields freeze before execution, terminal fields append exactly once.

Required claim-time snapshot:

- WorkOrder source ref/revision/digest and local execution projection ID;
- Employee, IdentityBinding and Agent IDs plus binding revision/digest;
- exact Runtime Profile/Instance, Harness Version, Model, ModelEndpoint/RuntimeEndpoint and CapacityLease references/digests;
- Session/worktree/sandbox references, input digest and start time;
- initiator and accountable actor references.

Required terminal snapshot:

- terminal status/time, attempt lineage, token/cost/usage snapshot;
- trace reference, error/failure classification;
- output content hash and ArtifactCandidate refs.

The daemon claim `auth_token` is a secret and must never be copied into the receipt.

### 5.5 Temporary result, Review and Promotion

Existing attachment storage and comment/message links remain transport and storage mechanisms. P2 adds a thin ArtifactCandidate identity over them:

```ts
type ArtifactCandidate = {
  artifact_candidate_id: string
  work_order_source_ref: string
  assignment_id: string
  run_id: string
  execution_receipt_id: string
  revision: number
  artifact_type: string
  title: string
  content_sha256: string
  durable_object_ref: string
  source_message_ids: string[]
  source_comment_ids: string[]
  created_at: string
  supersedes_candidate_id: string | null
}
```

The candidate writer copies or materialises the result into a HiveCrew-owned durable object namespace before committing the candidate. Existing attachment/message/comment IDs are provenance only; deleting them must not delete the candidate payload. Candidate revisions are immutable. A revision creates a new candidate and points to the previous candidate.

Review and promotion lifecycle use one append-only `artifact_event` ledger. Events are `submitted | changes_requested | approved | rejected | promotion_requested | promotion_failed | promotion_succeeded | authority_readback_confirmed` and include event ID, target candidate ID/revision, actor ref, feedback or error, time and idempotency key. A prior event is never updated or deleted.

`accepted` only authorises an explicit Promotion request. It does not create a Formal Artifact locally. Promotion success requires:

1. authority command receipt;
2. exact readback of the resulting Formal Artifact reference;
3. a local opaque link containing only promotion receipt/ref, not a copied formal record.

`promotion_succeeded` means only that the authority command returned a valid receipt. The UI may label a result as Formal Artifact only after `authority_readback_confirmed` has verified the exact source ref, revision and content digest. A successful command without readback remains visibly pending confirmation.

If Promotion fails, the accepted candidate remains accepted but unpromoted, the error remains visible, and retry uses the same idempotency key.

## 6. Frontend-to-runtime manifest

| Step | UI intent | Query / writer | Receipt | Exact refetch | Failure state |
| --- | --- | --- | --- | --- | --- |
| Open target | Deep-link one Employee/Binding/Agent/WorkOrder | Authority readers + existing Agent query | None | Exact four-object tuple | Invalid or conflicting tuple blocks composer/dispatch and shows provenance error |
| Talk | Send contextual message | Existing `createChatSession` + `sendChatMessage` | message ID, task ID | Session, messages, pending Run | Preserve message and immutable target tuple |
| Create/open WorkOrder | Confirm authority-backed work context | HiveCosm WorkOrder command/reader, never `POST /api/issues` as authority | authority command receipt | exact source ref/revision | Preserve draft; do not create local projection until receipt exists |
| Link local execution | Create/find HiveCrew execution projection | `IssueService.Create` through adapter | local Issue ID + external link ID | source link and Issue | Idempotent replay; no title-based identity |
| Assign | Confirm exact target and handoff | new thin `AssignmentService` + existing enqueue path | assignment ID + run ID | Assignment, Run, WorkOrder, binding | No toast-only success; preserve exact target and handoff |
| Execute | daemon claims and runs | existing task/daemon writers | task transitions | Run/task detail and transcript | Existing retry/cancel semantics remain |
| Record | freeze route and terminal outcome | new append-only Execution Receipt writer | receipt ID | exact receipt by run ID | Receipt failure is visible and blocks result acceptance |
| Submit result | register temporary output | attachment/comment/message input + independent durable ArtifactCandidate writer | candidate ID + digest | candidate detail/preview | Orphan storage objects reported; no silent success |
| Review | feedback/revise/accept | append-only artifact event writer | event ID | event history + current candidate | Preserve feedback; decision targets exact candidate revision |
| Promote | request formal result | HiveCosm promotion command | authority receipt | Formal Artifact ref | Accepted-but-unpromoted state; safe idempotent retry |

## 7. Pilot selection contract

The first real pilot is selected only when all conditions are observable:

1. one active HiveCosm WorkOrder source ref and revision can be read through the adapter;
2. one active Employee has exactly one current IdentityBinding to one current HiveCrew Agent UUID;
3. that Agent is callable in the selected workspace and has an admitted Runtime/Model route;
4. the WorkOrder scope permits a bounded, reversible, non-secret, non-production result;
5. the expected result can be represented as one small attachment/text artifact;
6. Promotion target is a non-production formal artifact authority with a readback path;
7. the owner can inspect the same journey in the visible candidate UI.

No pilot is chosen by a familiar employee name, a model ranking, a display-name match, or the first available Agent. If no object satisfies all conditions, P2 may complete tests and candidate code but the real-pilot acceptance remains open.

## 8. Behavioral test matrix for P2

### 8.1 Frontend behavior

1. Refreshing the stable deep link preserves the exact Employee/Binding/Agent/WorkOrder tuple.
2. A missing, stale, duplicate or conflicting binding disables dispatch and names the provenance failure.
3. Changing visible selection while a send is in flight does not change the captured target tuple.
4. WorkOrder command failure preserves the draft and does not create a local Issue projection.
5. Assignment success renders assignment, run and receipt IDs; a toast alone fails the test.
6. Assignment failure preserves WorkOrder, target and handoff note.
7. Run state and transcript are fetched by exact Run ID, never by first pending task.
8. Temporary result preview is tied to the exact receipt and digest.
9. Feedback targets the exact candidate revision and survives writer failure.
10. Accepted-but-unpromoted and promoted states are visually distinct; Formal Artifact appears only after authority readback.

### 8.2 Backend contracts

1. Replaying the same external command returns the same link, Issue, Assignment and Run IDs.
2. Two active Employees cannot bind the same Agent UUID; one Employee cannot have two active bindings in the selected scope.
3. Raw Issue update/query access cannot bypass `AssignmentService` validation.
4. Assignment and enqueue are atomic from the caller's perspective or return a compensatable failure state.
5. One Run produces at most one immutable Execution Receipt; terminal callback replay is idempotent.
6. Receipt freezes route/harness/model/endpoint/capacity refs and never stores secret values or claim tokens.
7. ArtifactCandidate content digest mismatch fails closed.
8. Deleting the source Issue, Comment, Message or Attachment cannot delete or alter a committed ArtifactCandidate payload.
9. Review replay is idempotent and cannot change a prior artifact event.
10. Promotion replay returns the same authority receipt/ref; local database never writes Formal Artifact state.
11. File upload DB-link failure is reported as an orphan/cleanup requirement, not a valid candidate.

### 8.3 Browser acceptance

The acceptance journey must be repeated in the candidate browser with visible stable URLs and no direct API substitution:

```text
Chat entry
 -> exact Employee/Binding/Agent/WorkOrder visible
 -> confirm Assignment
 -> real Run ID and live/terminal state
 -> immutable Receipt
 -> Temporary Artifact preview
 -> feedback and revision
 -> accept
 -> Promotion receipt
 -> Formal Artifact ref readback
```

Screenshots alone do not prove the chain. Acceptance evidence includes the visible journey, exact IDs, runtime/database receipts, current revision, test commands and rollback target.

## 9. First implementation wave and file boundary

P2 starts test-first in three non-overlapping fronts and joins once:

1. **Behavior contract tests**
   - focused frontend tests near Chat controller/page and the new WorkOrder context components;
   - focused server tests for source links, binding conflict, assignment/dispatch receipt, immutable receipt and artifact review/promotion references.
2. **Runtime loop**
   - migrations/query/service/handler files for external link, Assignment receipt, Execution Receipt, ArtifactCandidate and Review;
   - dependency-injected HiveCosm authority readers/command clients;
   - reuse existing Issue, Chat, Task, Comment and Attachment canonical writers.
3. **Owner surface**
   - central stable-link builder;
   - WorkOrder context/confirmation, exact Employee target, Run/Receipt, candidate review and Promotion readback inside existing Chat pane;
   - no broad sidebar or homepage redesign.

The exact code allowlist is frozen after the first failing tests identify the smallest compile boundary. Any need to modify Project CRUD, Employee registry, DGX/1421 release files, secrets or production configuration is out of P2 scope and requires a separate Work Order.

## 10. Audit findings incorporated

### Frontend source audit

- Current Chat is the smallest real execution surface and has the strongest stable-ID/message/task chain.
- Existing `RunConfirmModal` is reusable only as a visual/interaction pattern; its Issue update is not the WorkOrder assignment writer.
- `availableAgents[0]`, first Session and display-name fallbacks exist in adjacent code and are prohibited in the new journey.
- Inbox can later project attention items but is not needed to initiate the first P2 chain.

### Backend writer audit

- The existing local chain is `Issue -> assignee fields -> agent_task_queue -> daemon backend`.
- Canonical reusable writers are `IssueService.Create`, `TaskService.EnqueueTaskForIssueWithHandoff`, `TaskService.SendDirectChatMessage`, terminal task services, Comment and Attachment writers.
- Existing mutable task/usage/message data is insufficient as the B2 append-only Execution Receipt.
- An external-object link and command idempotency key are required; a local WorkOrder mirror is prohibited.

### Outcome source audit

- Current result carriers are task result/error, transcript messages, comments and attachments; they are execution evidence, not formal company outcomes.
- Task result is a terminal callback output; comments and transcripts are editable/deletable, usage is upserted, and attachments can be cascade-deleted. None is an immutable Receipt.
- ArtifactCandidate must copy the selected result into a durable HiveCrew object with its own digest and immutable revision; `artifact_event` records submission, review and promotion lifecycle append-only.
- Promotion and Formal Artifact remain HiveCosm authority writes and are recorded in HiveCrew only as opaque receipts/references.

## 11. P1 exit decision

This contract closes the design ambiguity, not the product gap. P1 may close after:

- exact source references have been read back;
- the Goal validator passes;
- independent phase review finds no authority or false-success blocker;
- the P1 owner-facing journal entry is appended, read back and verified.

P2 then begins with failing behavioral tests. No production apply, DGX/1421 change, company registry write, live external-agent dispatch or formal artifact promotion is authorised by this document alone.
