package workflow

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"
)

var (
	ErrDefinitionNotFound = errors.New("workflow definition not found")
	ErrInstanceNotFound   = errors.New("workflow instance not found")
	ErrInvalidDefinition  = errors.New("invalid workflow definition")
	ErrIllegalTransition  = errors.New("illegal workflow transition")
)

// Engine is a pure in-memory workflow orchestrator. It enforces risk-tier
// review gating, idempotent commands, and an append-only event log. It does
// not evaluate business conditions (callers supply evidence) and it does not
// write Task/Run/Project/Outcome state.
type Engine struct {
	mu          sync.Mutex
	definitions map[string]WorkflowDefinition
	// Versioned definitions keep an already-running graph pinned to the
	// immutable definition version that started it.
	versionedDefinitions map[string]WorkflowDefinition
	graphPlans           map[string]GraphExecutionPlan
	instances            map[string]*WorkflowInstance
	stageExecs           map[string][]StageExecution
	enteredAt            map[string]time.Time
	events               map[string][]Event
	byKey                map[string]Receipt
	now                  func() time.Time
}

func NewEngine() *Engine {
	return &Engine{
		definitions:          map[string]WorkflowDefinition{},
		versionedDefinitions: map[string]WorkflowDefinition{},
		graphPlans:           map[string]GraphExecutionPlan{},
		instances:            map[string]*WorkflowInstance{},
		stageExecs:           map[string][]StageExecution{},
		enteredAt:            map[string]time.Time{},
		events:               map[string][]Event{},
		byKey:                map[string]Receipt{},
		now:                  time.Now,
	}
}

// SetClock overrides the clock (test hook).
func (e *Engine) SetClock(fn func() time.Time) { e.now = fn }

func (e *Engine) clock() time.Time {
	if e.now == nil {
		return time.Now()
	}
	return e.now()
}

// ValidateDefinition returns an error if the definition is unusable.
func ValidateDefinition(d WorkflowDefinition) error {
	if d.ID == "" {
		return fmt.Errorf("%w: id required", ErrInvalidDefinition)
	}
	if d.Version <= 0 {
		return fmt.Errorf("%w: version must be positive", ErrInvalidDefinition)
	}
	if !d.Risk.Valid() {
		return fmt.Errorf("%w: invalid risk tier %q", ErrInvalidDefinition, d.Risk)
	}
	if len(d.Stages) == 0 {
		return fmt.Errorf("%w: at least one stage required", ErrInvalidDefinition)
	}
	for i, s := range d.Stages {
		if s.Name == "" {
			return fmt.Errorf("%w: stage %d name required", ErrInvalidDefinition, i)
		}
		if s.SLA < 0 {
			return fmt.Errorf("%w: stage %d negative SLA", ErrInvalidDefinition, i)
		}
	}
	return nil
}

// ValidateGraph enforces the stable, acyclic graph contract used by published
// definition versions. It does not execute nodes or resolve external agents.
func ValidateGraph(v WorkflowDefinitionVersion) error {
	if v.DefinitionID == "" || v.Version <= 0 || !v.Risk.Valid() || len(v.Graph.Nodes) == 0 {
		return fmt.Errorf("%w: graph identity, version, risk and nodes are required", ErrInvalidDefinition)
	}
	nodes := make(map[string]GraphNode, len(v.Graph.Nodes))
	for _, n := range v.Graph.Nodes {
		if n.ID == "" || n.Name == "" || !n.Kind.Valid() {
			return fmt.Errorf("%w: invalid graph node", ErrInvalidDefinition)
		}
		if _, exists := nodes[n.ID]; exists {
			return fmt.Errorf("%w: duplicate graph node %q", ErrInvalidDefinition, n.ID)
		}
		nodes[n.ID] = n
	}
	adj := make(map[string][]string, len(nodes))
	indegree := make(map[string]int, len(nodes))
	for _, edge := range v.Graph.Edges {
		if edge.ID == "" || edge.From == "" || edge.To == "" || edge.From == edge.To {
			return fmt.Errorf("%w: invalid graph edge", ErrInvalidDefinition)
		}
		if _, ok := nodes[edge.From]; !ok {
			return fmt.Errorf("%w: edge source %q missing", ErrInvalidDefinition, edge.From)
		}
		if _, ok := nodes[edge.To]; !ok {
			return fmt.Errorf("%w: edge target %q missing", ErrInvalidDefinition, edge.To)
		}
		adj[edge.From] = append(adj[edge.From], edge.To)
		indegree[edge.To]++
	}
	queue := make([]string, 0, len(nodes))
	for id := range nodes {
		if indegree[id] == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, next := range adj[id] {
			indegree[next]--
			if indegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if visited != len(nodes) {
		return fmt.Errorf("%w: graph contains a cycle", ErrInvalidDefinition)
	}
	return nil
}

func (e *Engine) Register(d WorkflowDefinition) error {
	if err := ValidateDefinition(d); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.definitions[d.ID] = d
	// Legacy definitions have no workspace scope. Published graph definitions
	// are registered separately with their explicit workspace key.
	e.versionedDefinitions[definitionVersionKey("", d.ID, d.Version)] = d
	return nil
}

func definitionVersionKey(workspaceID, definitionID string, version int) string {
	return fmt.Sprintf("%s\x00%s\x00%d", workspaceID, definitionID, version)
}

func (e *Engine) registerGraphPlan(plan GraphExecutionPlan) error {
	if err := ValidateDefinition(plan.Definition); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	key := definitionVersionKey(plan.WorkspaceID, plan.Definition.ID, plan.Definition.Version)
	if existing, ok := e.versionedDefinitions[key]; ok && len(existing.Stages) != len(plan.Definition.Stages) {
		return fmt.Errorf("%w: definition version already registered with different stage plan", ErrIllegalTransition)
	}
	if existing, ok := e.graphPlans[key]; ok && !reflect.DeepEqual(existing, plan) {
		return fmt.Errorf("%w: published graph version is immutable", ErrIllegalTransition)
	}
	e.versionedDefinitions[key] = plan.Definition
	e.graphPlans[key] = plan
	return nil
}

func (e *Engine) definitionForInstance(inst *WorkflowInstance) (WorkflowDefinition, bool) {
	if inst.WorkspaceID != "" {
		if def, ok := e.versionedDefinitions[definitionVersionKey(inst.WorkspaceID, inst.DefinitionID, inst.DefinitionVersion)]; ok {
			return def, true
		}
		// A legacy definition may still be attached to a workspace-scoped
		// instance. It is safe to use the legacy ID map only after the scoped
		// published lookup misses; never use another workspace's graph key.
		if def, ok := e.definitions[inst.DefinitionID]; ok {
			return def, true
		}
		return WorkflowDefinition{}, false
	}
	def, ok := e.versionedDefinitions[definitionVersionKey("", inst.DefinitionID, inst.DefinitionVersion)]
	if ok {
		return def, true
	}
	def, ok = e.definitions[inst.DefinitionID]
	return def, ok
}

// AttachWorkspace records the workspace reference on an in-memory instance
// after the command has been authorized by the HTTP boundary. It does not
// create or copy any project/task/employee state.
func (e *Engine) AttachWorkspace(instanceID, workspaceID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	inst, ok := e.instances[instanceID]
	if !ok {
		return false
	}
	inst.WorkspaceID = workspaceID
	return true
}

// replay returns the stored receipt for an already-applied idempotency key
// with Changed=false (the command must not re-apply).
func (e *Engine) replay(key string) (Receipt, bool) {
	r, ok := e.byKey[key]
	if !ok {
		return Receipt{}, false
	}
	r.Changed = false
	return r, true
}

// Start creates (or returns the existing) instance. Idempotent by key.
func (e *Engine) Start(defID, instanceID string, ctx ContextRef, key string) (WorkflowInstance, Receipt, error) {
	return e.start(defID, instanceID, ctx, "", key)
}

// StartForWorkspace applies the same idempotent start command while binding
// the in-memory instance and replay key to one workspace.
func (e *Engine) StartForWorkspace(defID, instanceID string, ctx ContextRef, workspaceID, key string) (WorkflowInstance, Receipt, error) {
	return e.start(defID, instanceID, ctx, workspaceID, key)
}

func (e *Engine) start(defID, instanceID string, ctx ContextRef, workspaceID, key string) (WorkflowInstance, Receipt, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r, ok := e.replay(key); ok {
		if inst, found := e.instances[r.InstanceID]; found {
			if workspaceID != "" && inst.WorkspaceID != "" && inst.WorkspaceID != workspaceID {
				return WorkflowInstance{}, Receipt{}, fmt.Errorf("%w: idempotency key belongs to another workspace", ErrIllegalTransition)
			}
			return *inst, r, nil
		}
	}
	def, ok := e.definitions[defID]
	if !ok {
		return WorkflowInstance{}, Receipt{}, ErrDefinitionNotFound
	}
	inst := &WorkflowInstance{
		ID:                instanceID,
		WorkspaceID:       workspaceID,
		DefinitionID:      def.ID,
		DefinitionVersion: def.Version,
		Context:           ctx,
		StageIndex:        0,
		Status:            StatusRunning,
	}
	e.instances[instanceID] = inst
	e.enteredAt[instanceID] = e.clock()
	receipt := Receipt{Command: "start", InstanceID: instanceID, IdempotencyKey: key, Accepted: true, Changed: true}
	e.byKey[key] = receipt
	e.append(instanceID, "workflow.started", "definition://"+def.ID, "system", key)
	return *inst, receipt, nil
}

func (e *Engine) startVersionForWorkspace(defID string, version int, instanceID string, ctx ContextRef, workspaceID, key string) (WorkflowInstance, Receipt, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r, ok := e.replay(key); ok {
		if inst, found := e.instances[r.InstanceID]; found {
			if workspaceID != "" && inst.WorkspaceID != "" && inst.WorkspaceID != workspaceID {
				return WorkflowInstance{}, Receipt{}, fmt.Errorf("%w: idempotency key belongs to another workspace", ErrIllegalTransition)
			}
			return *inst, r, nil
		}
	}
	def, ok := e.versionedDefinitions[definitionVersionKey(workspaceID, defID, version)]
	if !ok {
		return WorkflowInstance{}, Receipt{}, ErrDefinitionNotFound
	}
	inst := &WorkflowInstance{
		ID: instanceID, WorkspaceID: workspaceID, DefinitionID: def.ID,
		DefinitionVersion: def.Version, Context: ctx, StageIndex: 0, Status: StatusRunning,
	}
	e.instances[instanceID] = inst
	e.enteredAt[instanceID] = e.clock()
	receipt := Receipt{Command: "start", InstanceID: instanceID, IdempotencyKey: key, Accepted: true, Changed: true}
	e.byKey[key] = receipt
	e.append(instanceID, "workflow.started", "definition://"+def.ID+"/versions/"+fmt.Sprint(def.Version), "system", key)
	return *inst, receipt, nil
}

// Advance moves to the next stage, enforcing the risk gate. Idempotent by key.
func (e *Engine) Advance(instanceID string, ev AdvanceEvidence, key string) (WorkflowInstance, Receipt, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r, ok := e.replay(key); ok {
		if inst, found := e.instances[r.InstanceID]; found {
			return *inst, r, nil
		}
	}
	inst, ok := e.instances[instanceID]
	if !ok {
		return WorkflowInstance{}, Receipt{}, ErrInstanceNotFound
	}
	def, ok := e.definitionForInstance(inst)
	if !ok {
		return *inst, Receipt{Command: "advance", InstanceID: instanceID, IdempotencyKey: key, Reason: "definition not registered"}, ErrDefinitionNotFound
	}

	if inst.Status != StatusRunning {
		return *inst, Receipt{Command: "advance", InstanceID: instanceID, IdempotencyKey: key, Reason: fmt.Sprintf("instance is %s", inst.Status)}, ErrIllegalTransition
	}
	if inst.StageIndex >= len(def.Stages) {
		return *inst, Receipt{Command: "advance", InstanceID: instanceID, IdempotencyKey: key, Reason: "already completed"}, ErrIllegalTransition
	}
	if plan, graph := e.graphPlans[definitionVersionKey(inst.WorkspaceID, inst.DefinitionID, inst.DefinitionVersion)]; graph && inst.StageIndex < len(plan.Stages) {
		gate := plan.Stages[inst.StageIndex]
		if gate.RequiresReviewEvidence && !ev.ReviewPassed {
			receipt := e.rejectAdvance(instanceID, ev.ActorID, key, "graph stage requires explicit review evidence")
			return *inst, receipt, nil
		}
		if gate.RequiresDecisionEvidence && ev.DecisionOutcome == "" {
			receipt := e.rejectAdvance(instanceID, ev.ActorID, key, "graph stage requires explicit decision evidence")
			return *inst, receipt, nil
		}
	}

	// Risk gate. FAST auto-advances; STANDARD needs independent review;
	// OWNER needs explicit owner approval. No other signal substitutes.
	switch def.Risk {
	case RiskStandard:
		if !ev.ReviewPassed {
			receipt := e.rejectAdvance(instanceID, ev.ActorID, key, "independent review required")
			return *inst, receipt, nil
		}
	case RiskOwner:
		if !ev.OwnerApproved {
			receipt := e.rejectAdvance(instanceID, ev.ActorID, key, "owner approval required")
			return *inst, receipt, nil
		}
	}

	cur := def.Stages[inst.StageIndex]
	entered := e.enteredAt[instanceID]
	if entered.IsZero() {
		entered = e.clock()
	}
	e.stageExecs[instanceID] = append(e.stageExecs[instanceID], StageExecution{
		InstanceID:      instanceID,
		StageIndex:      inst.StageIndex,
		StageName:       cur.Name,
		EnteredAt:       entered,
		TaskID:          ev.TaskID,
		RunID:           ev.RunID,
		ActorID:         ev.ActorID,
		RuntimeID:       ev.RuntimeID,
		DecisionOutcome: ev.DecisionOutcome,
		Evidence:        ev.Notes,
	})

	inst.StageIndex++
	e.enteredAt[instanceID] = e.clock()
	if inst.StageIndex >= len(def.Stages) {
		inst.Status = StatusCompleted
	}

	receipt := Receipt{Command: "advance", InstanceID: instanceID, IdempotencyKey: key, Accepted: true, Changed: true}
	e.byKey[key] = receipt
	e.append(instanceID, "workflow.stage_advanced", "instance://"+instanceID, ev.ActorID, key)
	return *inst, receipt, nil
}

// rejectAdvance records a durable, read-only-visible control fact in the
// existing workflow event stream. It does not advance the instance or create a
// parallel approval registry. The detailed reason is returned in the control
// receipt; the event persists the accepted/rejected transition for refreshes.
func (e *Engine) rejectAdvance(instanceID, actorID, key, reason string) Receipt {
	receipt := Receipt{Command: "advance", InstanceID: instanceID, IdempotencyKey: key, Accepted: false, Reason: reason}
	e.byKey[key] = receipt
	e.append(instanceID, "workflow.advance_rejected", "control://advance", actorID, key)
	return receipt
}

// Pause pauses a running instance. Idempotent by key.
func (e *Engine) Pause(instanceID, key string) (WorkflowInstance, Receipt, error) {
	return e.lifecycle(instanceID, "pause", key, func(i *WorkflowInstance) error {
		if i.Status != StatusRunning {
			return fmt.Errorf("%w: cannot pause %s", ErrIllegalTransition, i.Status)
		}
		i.Status = StatusPaused
		return nil
	})
}

// Resume resumes a paused instance. Idempotent by key.
func (e *Engine) Resume(instanceID, key string) (WorkflowInstance, Receipt, error) {
	return e.lifecycle(instanceID, "resume", key, func(i *WorkflowInstance) error {
		if i.Status != StatusPaused {
			return fmt.Errorf("%w: cannot resume %s", ErrIllegalTransition, i.Status)
		}
		i.Status = StatusRunning
		return nil
	})
}

// Stop stops a running or paused instance. Idempotent by key.
func (e *Engine) Stop(instanceID, key string) (WorkflowInstance, Receipt, error) {
	return e.lifecycle(instanceID, "stop", key, func(i *WorkflowInstance) error {
		if i.Status != StatusRunning && i.Status != StatusPaused {
			return fmt.Errorf("%w: cannot stop %s", ErrIllegalTransition, i.Status)
		}
		i.Status = StatusStopped
		return nil
	})
}

// Fail marks a running instance failed with a reason. Idempotent by key.
func (e *Engine) Fail(instanceID, reason, key string) (WorkflowInstance, Receipt, error) {
	return e.lifecycle(instanceID, "fail", key, func(i *WorkflowInstance) error {
		if i.Status != StatusRunning {
			return fmt.Errorf("%w: cannot fail %s", ErrIllegalTransition, i.Status)
		}
		i.Status = StatusFailed
		return nil
	})
}

// Recover moves a failed instance back to a given stage. Idempotent by key.
func (e *Engine) Recover(instanceID string, stageIndex int, key string) (WorkflowInstance, Receipt, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r, ok := e.replay(key); ok {
		if inst, found := e.instances[r.InstanceID]; found {
			return *inst, r, nil
		}
	}
	inst, ok := e.instances[instanceID]
	if !ok {
		return WorkflowInstance{}, Receipt{}, ErrInstanceNotFound
	}
	def, ok := e.definitionForInstance(inst)
	if !ok {
		return *inst, Receipt{Command: "recover", InstanceID: instanceID, IdempotencyKey: key, Reason: "definition not registered"}, ErrDefinitionNotFound
	}
	if inst.Status != StatusFailed {
		return *inst, Receipt{Command: "recover", InstanceID: instanceID, IdempotencyKey: key, Reason: "not failed"}, ErrIllegalTransition
	}
	if stageIndex < 0 || stageIndex >= len(def.Stages) {
		return *inst, Receipt{Command: "recover", InstanceID: instanceID, IdempotencyKey: key, Reason: "stage index out of range"}, ErrIllegalTransition
	}
	inst.Status = StatusRunning
	inst.StageIndex = stageIndex
	e.enteredAt[instanceID] = e.clock()
	receipt := Receipt{Command: "recover", InstanceID: instanceID, IdempotencyKey: key, Accepted: true, Changed: true}
	e.byKey[key] = receipt
	e.append(instanceID, "workflow.recovered", "instance://"+instanceID, "system", key)
	return *inst, receipt, nil
}

// lifecycle applies a guarded status transition with idempotency.
func (e *Engine) lifecycle(instanceID, command, key string, apply func(*WorkflowInstance) error) (WorkflowInstance, Receipt, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if r, ok := e.replay(key); ok {
		if inst, found := e.instances[r.InstanceID]; found {
			return *inst, r, nil
		}
	}
	inst, ok := e.instances[instanceID]
	if !ok {
		return WorkflowInstance{}, Receipt{}, ErrInstanceNotFound
	}
	if err := apply(inst); err != nil {
		return *inst, Receipt{Command: command, InstanceID: instanceID, IdempotencyKey: key, Reason: err.Error()}, err
	}
	receipt := Receipt{Command: command, InstanceID: instanceID, IdempotencyKey: key, Accepted: true, Changed: true}
	e.byKey[key] = receipt
	e.append(instanceID, "workflow."+command, "instance://"+instanceID, "system", key)
	return *inst, receipt, nil
}

// Overdue reports whether the instance's current stage has exceeded its SLA.
// Only applies to running instances with a stage SLA > 0. now is caller-supplied.
func (e *Engine) Overdue(instanceID string, now time.Time) (stage string, overdueBy time.Duration, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	inst, found := e.instances[instanceID]
	if !found || inst.Status != StatusRunning {
		return "", 0, false
	}
	def, ok := e.definitionForInstance(inst)
	if !ok {
		return "", 0, false
	}
	if inst.StageIndex >= len(def.Stages) {
		return "", 0, false
	}
	cur := def.Stages[inst.StageIndex]
	if cur.SLA <= 0 {
		return "", 0, false
	}
	entered := e.enteredAt[instanceID]
	if entered.IsZero() {
		return "", 0, false
	}
	elapsed := now.Sub(entered)
	if elapsed <= cur.SLA {
		return "", 0, false
	}
	return cur.Name, elapsed - cur.SLA, true
}

func (e *Engine) append(instanceID, kind, sourceRef, actor, key string) {
	now := e.clock()
	seq := int64(len(e.events[instanceID]))
	e.events[instanceID] = append(e.events[instanceID], Event{
		Sequence:       seq,
		InstanceID:     instanceID,
		Kind:           kind,
		SourceRef:      sourceRef,
		Actor:          actor,
		OccurredAt:     now,
		ObservedAt:     now,
		IdempotencyKey: key,
	})
}

// Get returns the instance (read-only snapshot).
func (e *Engine) Get(instanceID string) (WorkflowInstance, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	i, ok := e.instances[instanceID]
	if !ok {
		return WorkflowInstance{}, false
	}
	return *i, true
}

// Events returns the append-only event log for an instance, in order.
func (e *Engine) Events(instanceID string) []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Event, len(e.events[instanceID]))
	copy(out, e.events[instanceID])
	return out
}

// StageExecutions returns the recorded stage executions, in order.
func (e *Engine) StageExecutions(instanceID string) []StageExecution {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]StageExecution, len(e.stageExecs[instanceID]))
	copy(out, e.stageExecs[instanceID])
	return out
}
