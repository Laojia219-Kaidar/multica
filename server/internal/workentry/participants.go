package workentry

import (
	"context"
	"sort"
	"strings"
)

// ProjectParticipant is one actor observed on a project through the kernel's
// append-only registration receipt ledger. It is a read-only projection: it
// never writes and never fabricates an identity. employee_id is present only
// for registered_employee actors (VC-02: external agents never impersonate a
// DE-* employee).
type ProjectParticipant struct {
	ActorType  ActorType `json:"actor_type"`
	ActorID    string    `json:"actor_id"`
	EmployeeID string    `json:"employee_id,omitempty"`
	CarrierID  string    `json:"carrier_id,omitempty"`
	RuntimeID  string    `json:"runtime_id,omitempty"`
	ModelRef   string    `json:"model_ref,omitempty"`
	BaseID     string    `json:"base_id,omitempty"`
	HostID     string    `json:"host_id,omitempty"`
	SessionID  string    `json:"session_id,omitempty"`
	TaskID     string    `json:"task_id,omitempty"`
}

// ProjectParticipantsResult is the project-scoped participant read model that
// feeds the VC-04 project page (actor_type / employee_id / carrier / runtime /
// model / base / host / session / task per project).
type ProjectParticipantsResult struct {
	Source       string               `json:"source"`
	ProjectID    string               `json:"project_id"`
	Participants []ProjectParticipant `json:"participants"`
}

// participantFromReceipt projects one receipt's actor identity into the
// participant read model.
func participantFromReceipt(r ReceiptRecord) ProjectParticipant {
	return ProjectParticipant{
		ActorType:  r.Actor.ActorType,
		ActorID:    r.Actor.ActorID,
		EmployeeID: r.Actor.EmployeeID,
		CarrierID:  r.Actor.CarrierID,
		RuntimeID:  r.Actor.RuntimeID,
		ModelRef:   r.Actor.ModelRef,
		BaseID:     r.Actor.BaseID,
		HostID:     r.Actor.HostID,
		SessionID:  r.Actor.SessionID,
		TaskID:     r.TaskID,
	}
}

// ProjectParticipants aggregates every actor who registered against a project
// through the kernel receipt ledger, scoped to the authenticated workspace.
// One participant row per (actor_type, actor_id, employee_id); repeated
// registrations for the same actor collapse to the earliest receipt.
func (s *Service) ProjectParticipants(ctx context.Context, workspaceID, projectID string) (*ProjectParticipantsResult, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(projectID) == "" {
		return nil, ErrInvalidRequest
	}
	if s == nil || s.store == nil {
		return nil, ErrUnavailable
	}
	list, err := s.store.ListProjectParticipants(ctx, workspaceID, projectID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]ProjectParticipant, 0, len(list))
	for _, p := range list {
		key := string(p.ActorType) + "\x00" + p.ActorID + "\x00" + p.EmployeeID
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ActorType != out[j].ActorType {
			return out[i].ActorType < out[j].ActorType
		}
		return out[i].ActorID < out[j].ActorID
	})
	return &ProjectParticipantsResult{
		Source:       "work_entry_participants",
		ProjectID:    projectID,
		Participants: out,
	}, nil
}
