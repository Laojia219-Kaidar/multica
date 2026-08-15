package workentry

import "sort"

// MemorySnapshot is the JSON-serializable state of a MemoryStore. The
// `multica work` CLI persists this snapshot across invocations so the offline
// candidate ledger (resolve → register → status) can be exercised end to end
// without a database. It is a spool snapshot, not a durable DB: it carries
// candidate receipts/events/inbox only.
type MemorySnapshot struct {
	Links       []ExternalWorkOrderLink `json:"links,omitempty"`
	Projects    []ProjectRef            `json:"projects,omitempty"`
	Issues      []IssueRef              `json:"issues,omitempty"`
	Receipts    []ReceiptRecord         `json:"receipts,omitempty"`
	Events      []EventRecord           `json:"events,omitempty"`
	Handoffs    []HandoffRecord         `json:"handoffs,omitempty"`
	Completions []CompletionRecord      `json:"completions,omitempty"`
	Inbox       []InboxItem             `json:"inbox,omitempty"`
	RepoMatches []RepoMatch             `json:"repo_matches,omitempty"`
	Sequence    int64                   `json:"sequence,omitempty"`
}

// Snapshot returns a deterministic deep copy of the current store state.
func (m *MemoryStore) Snapshot() MemorySnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	s := MemorySnapshot{Sequence: m.seq}
	s.Links = make([]ExternalWorkOrderLink, 0, len(m.links))
	for _, v := range m.links {
		s.Links = append(s.Links, *v)
	}
	sort.Slice(s.Links, func(i, j int) bool {
		return s.Links[i].WorkspaceID+s.Links[i].WorkOrderRef < s.Links[j].WorkspaceID+s.Links[j].WorkOrderRef
	})

	s.Projects = make([]ProjectRef, 0, len(m.projects))
	for _, v := range m.projects {
		s.Projects = append(s.Projects, *v)
	}
	sort.Slice(s.Projects, func(i, j int) bool {
		return s.Projects[i].WorkspaceID+s.Projects[i].ID < s.Projects[j].WorkspaceID+s.Projects[j].ID
	})

	s.Issues = make([]IssueRef, 0, len(m.issues))
	for _, v := range m.issues {
		s.Issues = append(s.Issues, *v)
	}
	sort.Slice(s.Issues, func(i, j int) bool {
		return s.Issues[i].WorkspaceID+s.Issues[i].ID < s.Issues[j].WorkspaceID+s.Issues[j].ID
	})

	s.Receipts = make([]ReceiptRecord, 0, len(m.receipts))
	for _, v := range m.receipts {
		s.Receipts = append(s.Receipts, *v)
	}
	sort.Slice(s.Receipts, func(i, j int) bool {
		return s.Receipts[i].WorkspaceID+s.Receipts[i].DedupeKey < s.Receipts[j].WorkspaceID+s.Receipts[j].DedupeKey
	})

	s.Events = make([]EventRecord, 0, len(m.events))
	for _, v := range m.events {
		s.Events = append(s.Events, *v)
	}
	sort.Slice(s.Events, func(i, j int) bool {
		if s.Events[i].Sequence != s.Events[j].Sequence {
			return s.Events[i].Sequence < s.Events[j].Sequence
		}
		return s.Events[i].ID < s.Events[j].ID
	})

	s.Handoffs = make([]HandoffRecord, 0, len(m.handoffs))
	for _, v := range m.handoffs {
		s.Handoffs = append(s.Handoffs, v)
	}
	sort.Slice(s.Handoffs, func(i, j int) bool { return s.Handoffs[i].WorkRef < s.Handoffs[j].WorkRef })

	s.Completions = make([]CompletionRecord, 0, len(m.completions))
	for _, v := range m.completions {
		s.Completions = append(s.Completions, v)
	}
	sort.Slice(s.Completions, func(i, j int) bool { return s.Completions[i].WorkRef < s.Completions[j].WorkRef })

	s.Inbox = make([]InboxItem, 0, len(m.inbox))
	for _, v := range m.inbox {
		s.Inbox = append(s.Inbox, v)
	}
	sort.Slice(s.Inbox, func(i, j int) bool { return s.Inbox[i].ID < s.Inbox[j].ID })

	s.RepoMatches = make([]RepoMatch, 0, len(m.repoMatchesMap))
	for _, v := range m.repoMatchesMap {
		s.RepoMatches = append(s.RepoMatches, v)
	}
	sort.Slice(s.RepoMatches, func(i, j int) bool {
		return s.RepoMatches[i].WorkspaceID+s.RepoMatches[i].Repo+s.RepoMatches[i].Revision+s.RepoMatches[i].Branch <
			s.RepoMatches[j].WorkspaceID+s.RepoMatches[j].Repo+s.RepoMatches[j].Revision+s.RepoMatches[j].Branch
	})
	return s
}

// Restore replaces the current store state with the snapshot contents.
func (m *MemoryStore) Restore(s MemorySnapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.links = make(map[string]*ExternalWorkOrderLink, len(s.Links))
	for i := range s.Links {
		v := s.Links[i]
		m.links[memKey(v.WorkspaceID, v.WorkOrderRef)] = &v
	}
	m.projects = make(map[string]*ProjectRef, len(s.Projects))
	for i := range s.Projects {
		v := s.Projects[i]
		m.projects[memKey(v.WorkspaceID, v.ID)] = &v
	}
	m.issues = make(map[string]*IssueRef, len(s.Issues))
	for i := range s.Issues {
		v := s.Issues[i]
		m.issues[memKey(v.WorkspaceID, v.ID)] = &v
	}
	m.receipts = make(map[string]*ReceiptRecord, len(s.Receipts))
	for i := range s.Receipts {
		v := s.Receipts[i]
		m.receipts[memKey(v.WorkspaceID, v.DedupeKey)] = &v
	}
	m.events = make(map[string]*EventRecord, len(s.Events))
	for i := range s.Events {
		v := s.Events[i]
		m.events[memKey(v.WorkspaceID, v.WorkRef, v.IdempotencyKey)] = &v
	}
	m.handoffs = make(map[string]HandoffRecord, len(s.Handoffs))
	for _, v := range s.Handoffs {
		m.handoffs[v.WorkRef] = v
	}
	m.completions = make(map[string]CompletionRecord, len(s.Completions))
	for _, v := range s.Completions {
		m.completions[v.WorkRef] = v
	}
	m.inbox = make(map[string]InboxItem, len(s.Inbox))
	for _, v := range s.Inbox {
		m.inbox[v.ID] = v
	}
	m.repoMatchesMap = make(map[string]RepoMatch, len(s.RepoMatches))
	for _, v := range s.RepoMatches {
		m.repoMatchesMap[memKey(v.WorkspaceID, v.Repo, v.Revision, v.Branch)] = v
	}
	m.seq = s.Sequence
}
