package workentry

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// MemoryStore is a concurrency-safe in-memory Store used by unit tests and as
// the offline candidate ledger. It implements every verb so the kernel
// semantics can be exercised without a database.
type MemoryStore struct {
	mu sync.Mutex

	links      map[string]*ExternalWorkOrderLink // workspaceID + "\x00" + workOrderRef
	projects   map[string]*ProjectRef            // workspaceID + "\x00" + projectID
	issues     map[string]*IssueRef              // workspaceID + "\x00" + issueID
	receipts   map[string]*ReceiptRecord         // workspaceID + "\x00" + dedupeKey
	events     map[string]*EventRecord           // workspaceID + "\x00" + workRef + "\x00" + idempotencyKey
	handoffs   map[string]HandoffRecord          // workRef
	completions map[string]CompletionRecord      // workRef
	inbox      map[string]InboxItem              // inboxID
	repoMatchesMap map[string]RepoMatch          // workspaceID + "\x00" + repo + "\x00" + revision + "\x00" + branch
	campaigns      map[string]*CampaignMatch     // workspaceID + "\x00" + upper(campaignRef)
	repoRefs       []RepoRef                     // repo ownership refs for inventory
	projectLeads   []ProjectLead                 // project owner/lead projections for steward
	seq        int64
}

// NewMemoryStore returns an empty in-memory Store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		links:       make(map[string]*ExternalWorkOrderLink),
		projects:    make(map[string]*ProjectRef),
		issues:      make(map[string]*IssueRef),
		receipts:    make(map[string]*ReceiptRecord),
		events:      make(map[string]*EventRecord),
		handoffs:    make(map[string]HandoffRecord),
		completions: make(map[string]CompletionRecord),
		inbox:       make(map[string]InboxItem),
		repoMatchesMap: make(map[string]RepoMatch),
		campaigns:      make(map[string]*CampaignMatch),
		repoRefs:       []RepoRef{},
		projectLeads:   []ProjectLead{},
	}
}

func memKey(parts ...string) string { return strings.Join(parts, "\x00") }

func (m *MemoryStore) LookupWorkOrder(_ context.Context, workspaceID, workOrderRef string) (*ExternalWorkOrderLink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.links[memKey(workspaceID, workOrderRef)]
	if l == nil {
		return nil, nil
	}
	cp := *l
	return &cp, nil
}

func (m *MemoryStore) LookupProject(_ context.Context, workspaceID, projectID string) (*ProjectRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.projects[memKey(workspaceID, projectID)]
	if p == nil {
		return nil, nil
	}
	cp := *p
	return &cp, nil
}

func (m *MemoryStore) LookupIssue(_ context.Context, workspaceID, issueID string) (*IssueRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.issues[memKey(workspaceID, issueID)]
	if i == nil {
		return nil, nil
	}
	cp := *i
	return &cp, nil
}

func (m *MemoryStore) LookupRepoRevisionBranch(_ context.Context, workspaceID, repo, revision, branch string) (*RepoMatch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memKey(workspaceID, repo, revision, branch)
	if r, ok := m.repoMatchesMap[k]; ok {
		cp := r
		return &cp, nil
	}
	return nil, nil
}

func (m *MemoryStore) SearchSimilar(_ context.Context, workspaceID, query string, limit int) ([]SimilarMatch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil, nil
	}
	type scored struct {
		match SimilarMatch
		score float64
	}
	var out []scored
	for _, p := range m.projects {
		if p.WorkspaceID != workspaceID {
			continue
		}
		if s := similarityScore(p.Title, query); s > 0 {
			out = append(out, scored{SimilarMatch{Kind: "project", RefID: p.ID, Title: p.Title, WorkspaceID: p.WorkspaceID, Similarity: s}, s})
		}
	}
	for _, i := range m.issues {
		if i.WorkspaceID != workspaceID {
			continue
		}
		if s := similarityScore(i.Title, query); s > 0 {
			out = append(out, scored{SimilarMatch{Kind: "issue", RefID: i.ID, Title: i.Title, WorkspaceID: i.WorkspaceID, Similarity: s}, s})
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].score > out[b].score })
	if limit <= 0 || limit > len(out) {
		limit = len(out)
	}
	res := make([]SimilarMatch, 0, limit)
	for i := 0; i < limit; i++ {
		res = append(res, out[i].match)
	}
	return res, nil
}

func (m *MemoryStore) GetReceipt(_ context.Context, workspaceID, dedupeKey string) (*ReceiptRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.receipts[memKey(workspaceID, dedupeKey)]
	if r == nil {
		return nil, nil
	}
	cp := *r
	return &cp, nil
}

func (m *MemoryStore) FindReceiptByWorkRef(_ context.Context, workspaceID, workRef string) (*ReceiptRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.receipts {
		if r.WorkspaceID == workspaceID && r.WorkRef == workRef {
			cp := *r
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *MemoryStore) PutWorkOrderLink(_ context.Context, link ExternalWorkOrderLink) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memKey(link.WorkspaceID, link.WorkOrderRef)
	if existing, ok := m.links[k]; ok {
		if existing.LinkedDigest != link.LinkedDigest || existing.LinkedRevision != link.LinkedRevision {
			return ErrConflict
		}
		return nil
	}
	cp := link
	m.links[k] = &cp
	return nil
}

func (m *MemoryStore) PutReceipt(_ context.Context, receipt ReceiptRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memKey(receipt.WorkspaceID, receipt.DedupeKey)
	if existing, ok := m.receipts[k]; ok {
		if existing.Digest != receipt.Digest {
			return ErrConflict
		}
		return nil
	}
	cp := receipt
	m.receipts[k] = &cp
	return nil
}

func (m *MemoryStore) AppendEvent(_ context.Context, event EventRecord) (*EventRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memKey(event.WorkspaceID, event.WorkRef, event.IdempotencyKey)
	if existing, ok := m.events[k]; ok {
		if !eventEqual(existing, &event) {
			return nil, ErrConflict
		}
		cp := *existing
		return &cp, nil
	}
	m.seq++
	cp := event
	cp.Sequence = m.seq
	m.events[k] = &cp
	return &cp, nil
}

func (m *MemoryStore) GetEvent(_ context.Context, workspaceID, workRef, idempotencyKey string) (*EventRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.events[memKey(workspaceID, workRef, idempotencyKey)]
	if e == nil {
		return nil, nil
	}
	cp := *e
	return &cp, nil
}

func (m *MemoryStore) UpsertHeartbeat(_ context.Context, hb HeartbeatRecord) error {
	// Presence is a projection; the memory store only tracks the latest value
	// in a tiny map for tests.
	m.mu.Lock()
	defer m.mu.Unlock()
	return nil
}

func (m *MemoryStore) SaveHandoff(_ context.Context, h HandoffRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handoffs[h.WorkRef] = h
	return nil
}

func (m *MemoryStore) SaveCompletion(_ context.Context, c CompletionRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.completions[c.WorkRef] = c
	return nil
}

func (m *MemoryStore) ListInbox(_ context.Context, workspaceID string) ([]InboxItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []InboxItem
	for _, it := range m.inbox {
		if it.WorkspaceID == workspaceID {
			out = append(out, it)
		}
	}
	return out, nil
}

func (m *MemoryStore) AttachInbox(_ context.Context, workspaceID, inboxID, projectID, issueID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.inbox[inboxID]
	if !ok || it.WorkspaceID != workspaceID {
		return ErrNotFound
	}
	// Attach means the unclaimed work now has a project lineage; remove it from
	// inbox (the receipt is updated by the service).
	delete(m.inbox, inboxID)
	return nil
}

func (m *MemoryStore) IgnoreInbox(_ context.Context, workspaceID, inboxID, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.inbox[inboxID]
	if !ok || it.WorkspaceID != workspaceID {
		return ErrNotFound
	}
	delete(m.inbox, inboxID)
	return nil
}

func (m *MemoryStore) CreateWork(_ context.Context, req CreateWorkRequest) (*CreateWorkResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if req.IssueID != "" {
		if i, ok := m.issues[memKey(req.WorkspaceID, req.IssueID)]; ok {
			return &CreateWorkResult{ProjectID: i.ProjectID, IssueID: i.ID}, nil
		}
	}
	projectID := req.ProjectID
	if projectID == "" {
		projectID = "proj-" + newID()
		m.projects[memKey(req.WorkspaceID, projectID)] = &ProjectRef{
			ID: projectID, WorkspaceID: req.WorkspaceID, Title: req.Title, Status: "planned",
		}
	}
	issueID := "issue-" + newID()
	m.issues[memKey(req.WorkspaceID, issueID)] = &IssueRef{
		ID: issueID, WorkspaceID: req.WorkspaceID, Title: req.Title, Status: "todo", ProjectID: projectID,
	}
	return &CreateWorkResult{ProjectID: projectID, IssueID: issueID}, nil
}

// Seed helpers for fixtures/tests.

func (m *MemoryStore) SeedProject(p ProjectRef) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := p
	m.projects[memKey(p.WorkspaceID, p.ID)] = &cp
}

func (m *MemoryStore) SeedIssue(i IssueRef) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := i
	m.issues[memKey(i.WorkspaceID, i.ID)] = &cp
}

func (m *MemoryStore) SeedWorkOrderLink(l ExternalWorkOrderLink) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := l
	m.links[memKey(l.WorkspaceID, l.WorkOrderRef)] = &cp
}

func (m *MemoryStore) SeedInbox(it InboxItem) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := it
	m.inbox[it.ID] = cp
}

// LookupCampaign implements campaignResolver (read-only G-series campaign →
// project resolution via project_resource / issue.metadata, zero migration).
func (m *MemoryStore) LookupCampaign(_ context.Context, workspaceID, campaignRef string) (*CampaignMatch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ref := NormalizeCampaignRef(campaignRef)
	if c, ok := m.campaigns[memKey(workspaceID, "campaign", ref)]; ok {
		return c, nil
	}
	return nil, nil
}

// SeedCampaign seeds a campaign → project match for tests.
func (m *MemoryStore) SeedCampaign(cm CampaignMatch) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ref := NormalizeCampaignRef(cm.CampaignRef)
	cm.CampaignRef = ref
	m.campaigns[memKey(cm.WorkspaceID, "campaign", ref)] = &cm
}

// SeedRepo seeds a repo ownership ref for inventory duplicate detection.
func (m *MemoryStore) SeedRepo(r RepoRef) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.repoRefs = append(m.repoRefs, r)
}

// InventorySnapshot implements inventorySource (read-only) for the duplicate/
// orphan diagnostic.
func (m *MemoryStore) InventorySnapshot(_ context.Context, workspaceID string) (*InventorySnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap := &InventorySnapshot{
		Projects: []ProjectRef{}, Issues: []IssueRef{},
		Links: []ExternalWorkOrderLink{}, Repos: []RepoRef{},
	}
	for _, p := range m.projects {
		if p.WorkspaceID == workspaceID {
			snap.Projects = append(snap.Projects, *p)
		}
	}
	for _, i := range m.issues {
		if i.WorkspaceID == workspaceID {
			snap.Issues = append(snap.Issues, *i)
		}
	}
	for _, l := range m.links {
		if l.WorkspaceID == workspaceID {
			snap.Links = append(snap.Links, *l)
		}
	}
	for _, r := range m.repoRefs {
		if r.WorkspaceID == workspaceID {
			snap.Repos = append(snap.Repos, r)
		}
	}
	return snap, nil
}

// SeedLead seeds a project owner/lead projection for steward diagnostics.
func (m *MemoryStore) SeedLead(l ProjectLead) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.projectLeads = append(m.projectLeads, l)
}

// StewardSnapshot implements stewardSource (read-only).
func (m *MemoryStore) StewardSnapshot(ctx context.Context, workspaceID string) (*StewardSnapshot, error) {
	inv, err := m.InventorySnapshot(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var leads []ProjectLead
	for _, l := range m.projectLeads {
		// projectLeads is not keyed by workspace; filter by project membership in snapshot.
		for _, p := range inv.Projects {
			if l.ProjectID == p.ID {
				leads = append(leads, l)
				break
			}
		}
	}
	return &StewardSnapshot{InventorySnapshot: *inv, ProjectLeads: leads}, nil
}

// SeedRepoMatch seeds a step-3 repo+revision+branch exact match for tests.
func (m *MemoryStore) SeedRepoMatch(r RepoMatch) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memKey(r.WorkspaceID, r.Repo, r.Revision, r.Branch)
	cp := r
	m.repoMatchesMap[k] = cp
}

func eventEqual(a, b *EventRecord) bool {
	if a == nil || b == nil {
		return false
	}
	pa, errA := CanonicalJSON(a.EventPayload)
	pb, errB := CanonicalJSON(b.EventPayload)
	if errA != nil || errB != nil {
		return false
	}
	return a.WorkRef == b.WorkRef &&
		a.SessionID == b.SessionID &&
		a.RunID == b.RunID &&
		a.EventType == b.EventType &&
		a.BlockerReason == b.BlockerReason &&
		a.Receiver == b.Receiver &&
		a.OccurredAt == b.OccurredAt &&
		string(pa) == string(pb)
}

// similarityScore is a tiny token-overlap similarity for fixtures (0..1).
func similarityScore(title, query string) float64 {
	title = strings.ToLower(title)
	t := strings.Fields(title)
	q := strings.Fields(query)
	if len(q) == 0 {
		return 0
	}
	hits := 0
	for _, qw := range q {
		for _, tw := range t {
			if strings.Contains(tw, qw) || strings.Contains(qw, tw) {
				hits++
				break
			}
		}
	}
	if hits == 0 {
		return 0
	}
	return float64(hits) / float64(len(q))
}
