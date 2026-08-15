// Package workflow implements the W4 workflow kernel contract
// (HIVECREW-WORKFLOW-MEMORY-OS-V1, Phase 1). It is a pure orchestration layer:
// it organizes and references existing Project/Issue/Task/Run/Outcome state,
// and never copies or becomes a second source of that business truth.
//
// Slice-W1 scope: in-memory, no schema, no migration. Persistence (Slice-W2)
// is gated on the Owner migration-counter decision.
package workflow

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RiskTier is the workflow risk classification (FAST/STANDARD/OWNER).
type RiskTier string

const (
	RiskFast     RiskTier = "fast"
	RiskStandard RiskTier = "standard"
	RiskOwner    RiskTier = "owner"
)

func (t RiskTier) Valid() bool {
	switch t {
	case RiskFast, RiskStandard, RiskOwner:
		return true
	default:
		return false
	}
}

// InstanceStatus is the closed workflow-instance state.
type InstanceStatus string

const (
	StatusRunning   InstanceStatus = "running"
	StatusPaused    InstanceStatus = "paused"
	StatusStopped   InstanceStatus = "stopped"
	StatusCompleted InstanceStatus = "completed"
	StatusFailed    InstanceStatus = "failed"
)

// Stage is one ordered step of a WorkflowDefinition.
type Stage struct {
	Name string        `json:"name"`
	SLA  time.Duration `json:"sla_ns,omitempty"` // 0 = no SLA
}

// WorkflowDefinition is a versioned workflow template.
type WorkflowDefinition struct {
	ID      string
	Version int
	Risk    RiskTier
	Stages  []Stage
}

// NodeKind is the minimal versioned graph contract. Graph definitions are
// candidate workflow configuration; they reference, but do not duplicate,
// HiveCrew Employee/Task/Run state.
type NodeKind string

const (
	NodeAgentTask NodeKind = "agent_task"
	NodeHumanTask NodeKind = "human_task"
	NodeApproval  NodeKind = "approval"
	NodeDecision  NodeKind = "decision"
)

func (k NodeKind) Valid() bool {
	switch k {
	case NodeAgentTask, NodeHumanTask, NodeApproval, NodeDecision:
		return true
	default:
		return false
	}
}

type AgentBinding struct {
	Mode         string   `json:"mode"` // fixed_employee, capability_pool, project_default, human
	EmployeeID   string   `json:"employee_id,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Role         string   `json:"role,omitempty"`
	Capability   string   `json:"capability,omitempty"`
}

// GraphPosition is presentation metadata for a published graph. It is
// persisted with the immutable version so the visual workflow can be reopened
// without deriving its layout from node order.
type GraphPosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type GraphNode struct {
	ID           string         `json:"id"`
	Kind         NodeKind       `json:"kind"`
	Name         string         `json:"name"`
	AgentBinding *AgentBinding  `json:"agent_binding,omitempty"`
	Position     *GraphPosition `json:"position,omitempty"`
}

type GraphEdge struct {
	ID   string `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
	When string `json:"when,omitempty"`
}

type WorkflowGraph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// WorkflowDefinitionVersion is immutable once published. It is deliberately
// separate from WorkflowDefinition's legacy stage representation so the
// designer can evolve without creating a second runtime engine.
type WorkflowDefinitionVersion struct {
	DefinitionID string        `json:"definition_id"`
	WorkspaceID  string        `json:"workspace_id"`
	ProjectID    string        `json:"project_id,omitempty"`
	Version      int           `json:"version"`
	Risk         RiskTier      `json:"risk"`
	Stages       []Stage       `json:"stages"`
	Graph        WorkflowGraph `json:"graph"`
	Digest       string        `json:"digest"`
	CreatedAt    time.Time     `json:"created_at"`
	PublishedAt  time.Time     `json:"published_at"`
}

// ValidatePublishedGraph enforces the closed graph contract at the publish
// boundary. A published graph is immutable, so accepting an invalid graph
// here would permanently create an unusable workflow definition version.
func (v WorkflowDefinitionVersion) ValidatePublishedGraph() error {
	if v.DefinitionID == "" || v.WorkspaceID == "" {
		return fmt.Errorf("definition_id and workspace_id are required")
	}
	if v.Version < 1 {
		return fmt.Errorf("version must be positive")
	}
	if !v.Risk.Valid() {
		return fmt.Errorf("invalid risk %q", v.Risk)
	}
	if len(v.Graph.Nodes) == 0 {
		return fmt.Errorf("graph must contain at least one node")
	}
	nodes := make(map[string]GraphNode, len(v.Graph.Nodes))
	for _, n := range v.Graph.Nodes {
		if n.ID == "" || n.Name == "" {
			return fmt.Errorf("graph nodes require id and name")
		}
		if _, exists := nodes[n.ID]; exists {
			return fmt.Errorf("duplicate graph node %q", n.ID)
		}
		if !n.Kind.Valid() {
			return fmt.Errorf("invalid graph node kind %q", n.Kind)
		}
		if err := validateAgentBinding(n); err != nil {
			return fmt.Errorf("node %q: %w", n.ID, err)
		}
		nodes[n.ID] = n
	}
	indegree := make(map[string]int, len(nodes))
	adj := make(map[string][]string, len(nodes))
	seenEdges := make(map[string]struct{}, len(v.Graph.Edges))
	for _, e := range v.Graph.Edges {
		if e.ID == "" || e.From == "" || e.To == "" {
			return fmt.Errorf("graph edges require id, from and to")
		}
		if e.From == e.To {
			return fmt.Errorf("graph edge %q is self-referential", e.ID)
		}
		if _, ok := nodes[e.From]; !ok {
			return fmt.Errorf("graph edge %q references unknown from node %q", e.ID, e.From)
		}
		if _, ok := nodes[e.To]; !ok {
			return fmt.Errorf("graph edge %q references unknown to node %q", e.ID, e.To)
		}
		key := e.From + "\x00" + e.To
		if _, exists := seenEdges[key]; exists {
			return fmt.Errorf("duplicate graph edge %q -> %q", e.From, e.To)
		}
		seenEdges[key] = struct{}{}
		adj[e.From] = append(adj[e.From], e.To)
		indegree[e.To]++
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
		return fmt.Errorf("graph must be acyclic")
	}
	return nil
}

func validateAgentBinding(n GraphNode) error {
	if n.Kind == NodeHumanTask {
		if n.AgentBinding != nil && n.AgentBinding.Mode != "human" {
			return fmt.Errorf("human_task binding must use human mode")
		}
		return nil
	}
	if n.Kind != NodeAgentTask {
		if n.AgentBinding != nil {
			return fmt.Errorf("agent binding is only allowed on task nodes")
		}
		return nil
	}
	if n.AgentBinding == nil {
		return fmt.Errorf("agent_task requires agent_binding")
	}
	switch n.AgentBinding.Mode {
	case "fixed_employee":
		if n.AgentBinding.EmployeeID == "" {
			return fmt.Errorf("fixed_employee requires employee_id")
		}
	case "capability_pool":
		if len(n.AgentBinding.Capabilities) == 0 {
			return fmt.Errorf("capability_pool requires capabilities")
		}
	case "role_pool":
		if n.AgentBinding.Role == "" {
			return fmt.Errorf("role_pool requires role")
		}
	case "project_default":
	default:
		return fmt.Errorf("invalid agent binding mode %q", n.AgentBinding.Mode)
	}
	return nil
}

// ContextRef references the business objects a workflow instance is about,
// WITHOUT copying their state.
type ContextRef struct {
	ProjectID string
	IssueID   string
	OutcomeID string
}

// WorkflowInstance is one running instance of a definition.
type WorkflowInstance struct {
	ID string
	// WorkspaceID scopes the orchestration record to the HiveCrew workspace.
	// It is a reference only; Project/Issue/Task/Run/Employee remain owned by
	// their respective authorities.
	WorkspaceID       string
	DefinitionID      string
	DefinitionVersion int
	Context           ContextRef
	StageIndex        int
	Status            InstanceStatus
}

// StageExecution records one stage execution with exact Task/Run/actor/runtime
// and evidence references.
type StageExecution struct {
	InstanceID      string
	StageIndex      int
	StageName       string
	EnteredAt       time.Time
	TaskID          string
	RunID           string
	ActorID         string
	RuntimeID       string
	DecisionOutcome string
	Evidence        []string
}

// Event is an append-only fact event with an idempotency key.
type Event struct {
	Sequence       int64
	InstanceID     string
	Kind           string
	SourceRef      string
	Actor          string
	OccurredAt     time.Time
	ObservedAt     time.Time
	IdempotencyKey string
}

// Receipt is a command receipt. Changed=false means the command was already
// applied under the same idempotency key (no double transition).
type Receipt struct {
	Command        string
	InstanceID     string
	IdempotencyKey string
	Accepted       bool
	Changed        bool
	Reason         string
}

// AdvanceEvidence is the caller-supplied proof for a stage advance. The engine
// only enforces the risk gate; it does not invent business conditions.
type AdvanceEvidence struct {
	ReviewPassed    bool // required for STANDARD
	OwnerApproved   bool // required for OWNER
	TaskID          string
	RunID           string
	ActorID         string
	RuntimeID       string
	DecisionOutcome string // required for a graph decision stage
	Notes           []string
}

// ---------------------------------------------------------------------------
// WeChat content production node contract (HIVECREW-WECHAT-REAL-OPERATIONS-V1
// / WO-10R, contract-freeze). Pure, side-effect-free mirror of the TypeScript
// contract (packages/core/workflow/content-node-contract.ts and
// packages/core/api/workflow.ts). It creates NO Task/Run/Artifact/Outcome
// authority: the existing Task/Run + CompanyOps Artifact/Outcome authorities
// remain the only execution and promotion path. Caller-supplied execution
// proof is never accepted as authority here.
// ---------------------------------------------------------------------------

const (
	// WechatContentContractSchemaVersion is the frozen node-contract version.
	WechatContentContractSchemaVersion = "hivecrew.wechat-content-node-contract.v1"
	// WechatContentProductionRequestSchemaVersion is the frozen request DTO version.
	WechatContentProductionRequestSchemaVersion = "hivecrew.wechat-content-production-request.v1"
	// WechatContentChannel is the single channel this template owns.
	WechatContentChannel = "wechat"
	// WechatContentHandoffNoteMaxBytes mirrors the existing CompanyOps
	// assignment handler cap (32 << 10 bytes of UTF-8).
	WechatContentHandoffNoteMaxBytes = 32 << 10
)

// WechatContentNodeKey is one of the four immutable content-production nodes.
type WechatContentNodeKey string

const (
	WechatContentNodeResearchMaterialPackage  WechatContentNodeKey = "research-material-package"
	WechatContentNodeArticleDraft             WechatContentNodeKey = "article-draft"
	WechatContentNodeEditorialReviewReport    WechatContentNodeKey = "editorial-review-report"
	WechatContentNodeWechatPublicationPackage WechatContentNodeKey = "wechat-publication-package"
)

// wechatContentNodeOrder is the frozen prerequisite order.
var wechatContentNodeOrder = []WechatContentNodeKey{
	WechatContentNodeResearchMaterialPackage,
	WechatContentNodeArticleDraft,
	WechatContentNodeEditorialReviewReport,
	WechatContentNodeWechatPublicationPackage,
}

// Frozen lineage authority metadata. Each lineage member's authority is a
// contract constant, never a caller-chosen string: it names the EXISTING
// authority that owns that lineage member.
const (
	WechatContentLineageAuthorityIssue      = "existing Issue authority (issue table / server/migrations/001_init.up.sql)"
	WechatContentLineageAuthorityAssignment = "CompanyOps assignment (Dispatch -> agent_task_queue)"
	WechatContentLineageAuthorityTask       = "agent_task_queue canonical Task"
	WechatContentLineageAuthorityRun        = "agent_runtime canonical Run"
	WechatContentLineageAuthorityCandidate  = "CompanyOps ArtifactCandidate (MaterializeCompletedTask)"
	WechatContentLineageAuthorityOutcome    = "CompanyOps Outcome (promotion + readback)"
)

var (
	wechatWorkOrderSourceRefPattern = regexp.MustCompile(
		`^hive://hivecosm/delivery/project/([A-Za-z0-9][A-Za-z0-9@._:-]{0,191})/work-order/[A-Za-z0-9][A-Za-z0-9@._:-]{0,191}$`,
	)
	wechatUUIDPattern         = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	wechatSHA256DigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	// wechatRFC3339Pattern mirrors the TS ISO_DATETIME_PATTERN exactly so all
	// three layers accept and reject the same deadline strings.
	wechatRFC3339Pattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-]\d{2}:\d{2})$`)
	// wechatRFC3339OffsetPattern captures the numeric offset so its range can
	// be validated explicitly: time.Parse(RFC3339Nano) checks the shape but
	// does NOT range-check the offset hour/minute, so without this Go would
	// accept +24:00 / +08:60 while the TS and Zod layers reject them.
	wechatRFC3339OffsetPattern = regexp.MustCompile(`[+-](\d{2}):(\d{2})$`)
)

// WechatContentAuthorityContext is the existing CompanyOps authority-context
// reference set. It identifies; it does not authorize (P0-GATE-02).
type WechatContentAuthorityContext struct {
	WorkOrderSourceRef string `json:"work_order_source_ref"`
	EmployeeID         string `json:"employee_id"`
	IdentityBindingID  string `json:"identity_binding_id"`
	AgentID            string `json:"agent_id"`
	SessionID          string `json:"session_id"`
}

// WechatContentDefinitionBinding binds the request to one immutable published
// workflow definition version.
type WechatContentDefinitionBinding struct {
	DefinitionID string `json:"definition_id"`
	Version      int    `json:"version"`
	Digest       string `json:"digest"`
}

// WechatContentBrief is the content production brief. HandoffNote is the
// exact work description delivered to the executing Agent; it matches the
// existing CompanyOps assignment Handoff semantics (trimmed non-empty, max
// 32 KiB UTF-8). The server computes input_digest from it; callers never
// supply or choose an authority digest.
type WechatContentBrief struct {
	Subject        string   `json:"subject"`
	Objective      string   `json:"objective"`
	Audience       string   `json:"audience"`
	SourceRefs     []string `json:"source_refs"`
	Tone           string   `json:"tone"`
	Deadline       string   `json:"deadline"`
	ApprovalPolicy string   `json:"approval_policy"`
	HandoffNote    string   `json:"handoff_note"`
}

// WechatContentProductionRequest references project/work-order sources, one
// frozen published definition version, and a content brief. It carries no
// execution or artifact proof.
type WechatContentProductionRequest struct {
	SchemaVersion  string                         `json:"schema_version"`
	Channel        string                         `json:"channel"`
	ProjectID      string                         `json:"project_id"`
	Authority      WechatContentAuthorityContext  `json:"authority"`
	Definition     WechatContentDefinitionBinding `json:"definition"`
	Brief          WechatContentBrief             `json:"brief"`
	IdempotencyKey string                         `json:"idempotency_key"`
}

// WechatContentLineageMember is one member of a node's durable lineage.
type WechatContentLineageMember struct {
	Required  bool   `json:"required"`
	Authority string `json:"authority"`
}

// WechatContentNodeLineage is the six-member Issue/Assignment/Task/Run/
// Candidate/Outcome lineage shape every content node owns.
type WechatContentNodeLineage struct {
	Issue      WechatContentLineageMember `json:"issue"`
	Assignment WechatContentLineageMember `json:"assignment"`
	Task       WechatContentLineageMember `json:"task"`
	Run        WechatContentLineageMember `json:"run"`
	Candidate  WechatContentLineageMember `json:"candidate"`
	Outcome    WechatContentLineageMember `json:"outcome"`
}

// frozenWechatContentNodeLineage returns the frozen six-member lineage with
// the contract-constant authorities.
func frozenWechatContentNodeLineage() WechatContentNodeLineage {
	return WechatContentNodeLineage{
		Issue:      WechatContentLineageMember{Required: true, Authority: WechatContentLineageAuthorityIssue},
		Assignment: WechatContentLineageMember{Required: true, Authority: WechatContentLineageAuthorityAssignment},
		Task:       WechatContentLineageMember{Required: true, Authority: WechatContentLineageAuthorityTask},
		Run:        WechatContentLineageMember{Required: true, Authority: WechatContentLineageAuthorityRun},
		Candidate:  WechatContentLineageMember{Required: true, Authority: WechatContentLineageAuthorityCandidate},
		Outcome:    WechatContentLineageMember{Required: true, Authority: WechatContentLineageAuthorityOutcome},
	}
}

// WechatContentNodeContract is one frozen, immutable node contract.
// RequiredUpstream is a pointer so the first node's wire value is JSON null
// (nil), never an empty string masquerading as wire parity with the TS
// `required_upstream: null`. Lineage is optional on a caller-submitted plan
// entry, but when present it must equal the frozen contract constants.
type WechatContentNodeContract struct {
	Key              WechatContentNodeKey      `json:"key"`
	Order            int                       `json:"order"`
	RequiredUpstream *WechatContentNodeKey     `json:"required_upstream"`
	ArtifactKind     string                    `json:"artifact_kind"`
	ReviewRule       string                    `json:"review_rule"`
	Lineage          *WechatContentNodeLineage `json:"lineage,omitempty"`
}

func wechatContentNodeKeyPtr(k WechatContentNodeKey) *WechatContentNodeKey {
	return &k
}

func wechatContentNodeKeyEqual(a, b *WechatContentNodeKey) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// WechatContentNodeContracts returns the frozen four-node sequence.
func WechatContentNodeContracts() []WechatContentNodeContract {
	lineage := frozenWechatContentNodeLineage()
	return []WechatContentNodeContract{
		{Key: WechatContentNodeResearchMaterialPackage, Order: 1, RequiredUpstream: nil, ArtifactKind: "wechat.research-material-package.v1", ReviewRule: "auto_accept", Lineage: &lineage},
		{Key: WechatContentNodeArticleDraft, Order: 2, RequiredUpstream: wechatContentNodeKeyPtr(WechatContentNodeResearchMaterialPackage), ArtifactKind: "wechat.article-draft.v1", ReviewRule: "editorial_review", Lineage: &lineage},
		{Key: WechatContentNodeEditorialReviewReport, Order: 3, RequiredUpstream: wechatContentNodeKeyPtr(WechatContentNodeArticleDraft), ArtifactKind: "wechat.editorial-review-report.v1", ReviewRule: "approval_gate", Lineage: &lineage},
		{Key: WechatContentNodeWechatPublicationPackage, Order: 4, RequiredUpstream: wechatContentNodeKeyPtr(WechatContentNodeEditorialReviewReport), ArtifactKind: "wechat.wechat-publication-package.v1", ReviewRule: "owner_approval", Lineage: &lineage},
	}
}

func isWechatContentNodeKey(k WechatContentNodeKey) bool {
	switch k {
	case WechatContentNodeResearchMaterialPackage, WechatContentNodeArticleDraft,
		WechatContentNodeEditorialReviewReport, WechatContentNodeWechatPublicationPackage:
		return true
	default:
		return false
	}
}

func frozenWechatContentNode(k WechatContentNodeKey) (WechatContentNodeContract, bool) {
	for _, n := range WechatContentNodeContracts() {
		if n.Key == k {
			return n, true
		}
	}
	return WechatContentNodeContract{}, false
}

func formatWechatNodeKeyPtr(k *WechatContentNodeKey) string {
	if k == nil {
		return "null"
	}
	return fmt.Sprintf("%q", string(*k))
}

// ValidateWechatContentProductionRequest is the pure, fail-closed validator
// for a WeChat content production request. A nil return means the request is
// contract-valid; any non-nil error fails closed. It never mutates state.
func ValidateWechatContentProductionRequest(req WechatContentProductionRequest) error {
	var errs []error
	if req.SchemaVersion != WechatContentProductionRequestSchemaVersion {
		errs = append(errs, fmt.Errorf("unsupported schema_version %q", req.SchemaVersion))
	}
	if req.Channel != WechatContentChannel {
		errs = append(errs, fmt.Errorf("unsupported channel %q", req.Channel))
	}
	if strings.TrimSpace(req.ProjectID) == "" {
		errs = append(errs, fmt.Errorf("project_id is required"))
	}
	errs = append(errs, validateWechatContentAuthority(req.Authority, req.ProjectID))
	errs = append(errs, validateWechatContentDefinition(req.Definition))
	errs = append(errs, validateWechatContentBrief(req.Brief))
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		errs = append(errs, fmt.Errorf("idempotency_key is required"))
	}
	return errors.Join(errs...)
}

func validateWechatContentAuthority(a WechatContentAuthorityContext, projectID string) error {
	var errs []error
	match := wechatWorkOrderSourceRefPattern.FindStringSubmatch(a.WorkOrderSourceRef)
	if match == nil {
		errs = append(errs, fmt.Errorf("work_order_source_ref must be hive://hivecosm/delivery/project/{project}/work-order/{work-order}"))
	} else if match[1] != projectID {
		errs = append(errs, fmt.Errorf("cross-project authority mismatch: work_order_source_ref project %q != project_id %q", match[1], projectID))
	}
	if strings.TrimSpace(a.EmployeeID) == "" {
		errs = append(errs, fmt.Errorf("employee_id is required"))
	}
	if strings.TrimSpace(a.IdentityBindingID) == "" {
		errs = append(errs, fmt.Errorf("identity_binding_id is required"))
	}
	if !wechatUUIDPattern.MatchString(a.AgentID) {
		errs = append(errs, fmt.Errorf("agent_id must be a UUID"))
	}
	if !wechatUUIDPattern.MatchString(a.SessionID) {
		errs = append(errs, fmt.Errorf("session_id must be a UUID"))
	}
	return errors.Join(errs...)
}

func validateWechatContentDefinition(d WechatContentDefinitionBinding) error {
	var errs []error
	if strings.TrimSpace(d.DefinitionID) == "" {
		errs = append(errs, fmt.Errorf("definition_id is required"))
	}
	if d.Version < 1 {
		errs = append(errs, fmt.Errorf("version must be a positive integer"))
	}
	if !wechatSHA256DigestPattern.MatchString(d.Digest) {
		errs = append(errs, fmt.Errorf("digest must be sha256:{64 hex}"))
	}
	return errors.Join(errs...)
}

func validateWechatContentBrief(b WechatContentBrief) error {
	var errs []error
	for name, value := range map[string]string{
		"subject": b.Subject, "objective": b.Objective,
		"audience": b.Audience, "tone": b.Tone,
	} {
		if strings.TrimSpace(value) == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
	}
	if len(b.SourceRefs) == 0 {
		errs = append(errs, fmt.Errorf("source_refs must contain at least one non-empty string"))
	} else {
		for _, ref := range b.SourceRefs {
			if strings.TrimSpace(ref) == "" {
				errs = append(errs, fmt.Errorf("source_refs entries must be non-empty"))
				break
			}
		}
	}
	if err := validateWechatContentDeadline(b.Deadline); err != nil {
		errs = append(errs, err)
	}
	if b.ApprovalPolicy != "owner_approval" && b.ApprovalPolicy != "editorial_review" {
		errs = append(errs, fmt.Errorf("approval_policy must be owner_approval or editorial_review"))
	}
	if strings.TrimSpace(b.HandoffNote) == "" {
		errs = append(errs, fmt.Errorf("handoff_note is required and must describe the work to dispatch"))
	} else if len(b.HandoffNote) > WechatContentHandoffNoteMaxBytes {
		errs = append(errs, fmt.Errorf("handoff_note must be at most %d UTF-8 bytes", WechatContentHandoffNoteMaxBytes))
	}
	return errors.Join(errs...)
}

// validateWechatContentDeadline enforces the same shape + real-calendar
// semantics as the TS isValidRfc3339Datetime: RFC3339 with a mandatory
// timezone (Z or numeric offset), no leap seconds, real calendar components,
// and a numeric offset within 00:00-23:59.
func validateWechatContentDeadline(value string) error {
	if !wechatRFC3339Pattern.MatchString(value) {
		return fmt.Errorf("deadline must be an RFC3339 datetime with timezone (Z or numeric offset)")
	}
	if match := wechatRFC3339OffsetPattern.FindStringSubmatch(value); match != nil {
		offsetHour, _ := strconv.Atoi(match[1])
		offsetMinute, _ := strconv.Atoi(match[2])
		if offsetHour > 23 || offsetMinute > 59 {
			return fmt.Errorf("deadline numeric offset must be within 00:00-23:59")
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return fmt.Errorf("deadline must be a valid RFC3339 datetime")
	}
	return nil
}

// ValidateWechatContentNodePlan is the pure, fail-closed validator for a node
// plan against the frozen four-node contract. It rejects duplicate, unknown,
// missing, and altered nodes, broken prerequisites, and non-frozen lineage.
// It never mutates state.
func ValidateWechatContentNodePlan(nodes []WechatContentNodeContract) error {
	if len(nodes) == 0 {
		return fmt.Errorf("node plan must contain the four frozen nodes")
	}
	var errs []error
	seen := make(map[WechatContentNodeKey]int, len(wechatContentNodeOrder))
	submitted := make([]WechatContentNodeKey, 0, len(wechatContentNodeOrder))
	for i, n := range nodes {
		if !isWechatContentNodeKey(n.Key) {
			errs = append(errs, fmt.Errorf("unknown node key %q", n.Key))
			continue
		}
		if prev, ok := seen[n.Key]; ok {
			errs = append(errs, fmt.Errorf("duplicate node %q (first at index %d)", n.Key, prev))
			continue
		}
		seen[n.Key] = i
		submitted = append(submitted, n.Key)

		frozen, ok := frozenWechatContentNode(n.Key)
		if !ok {
			continue
		}
		if n.ArtifactKind != frozen.ArtifactKind {
			errs = append(errs, fmt.Errorf("node %q artifact_kind altered from %q", n.Key, frozen.ArtifactKind))
		}
		if !wechatContentNodeKeyEqual(n.RequiredUpstream, frozen.RequiredUpstream) {
			errs = append(errs, fmt.Errorf("node %q required_upstream altered from %s", n.Key, formatWechatNodeKeyPtr(frozen.RequiredUpstream)))
		}
		if n.ReviewRule != frozen.ReviewRule {
			errs = append(errs, fmt.Errorf("node %q review_rule altered from %q", n.Key, frozen.ReviewRule))
		}
		if n.Order != frozen.Order {
			errs = append(errs, fmt.Errorf("node %q order altered from %d", n.Key, frozen.Order))
		}
		if n.Lineage != nil && frozen.Lineage != nil && *n.Lineage != *frozen.Lineage {
			errs = append(errs, fmt.Errorf("node %q lineage altered from the frozen contract constants", n.Key))
		}
	}
	for _, k := range wechatContentNodeOrder {
		if _, ok := seen[k]; !ok {
			errs = append(errs, fmt.Errorf("missing frozen node %q", k))
		}
	}
	for i, k := range submitted {
		frozen, ok := frozenWechatContentNode(k)
		if !ok || frozen.RequiredUpstream == nil {
			continue
		}
		upIdx := -1
		for j, s := range submitted {
			if s == *frozen.RequiredUpstream {
				upIdx = j
				break
			}
		}
		if upIdx == -1 {
			errs = append(errs, fmt.Errorf("node %q is missing its upstream %q", k, *frozen.RequiredUpstream))
		} else if upIdx >= i {
			errs = append(errs, fmt.Errorf("node %q precedes its upstream %q", k, *frozen.RequiredUpstream))
		}
	}
	return errors.Join(errs...)
}
