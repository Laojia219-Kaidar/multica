package service

import (
	"context"
	"errors"
	"testing"

	"github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/util"
)

func capacityCandidateFor(req CompanyOpsAssignmentRequest, quota int64, health string, used, limit int) *metrics.CapacityCandidate {
	return &metrics.CapacityCandidate{
		AgentID:          util.UUIDToString(req.LocalAgentID),
		Provider:         "qwen",
		Plan:             "Qwen Token Plan",
		Base:             "dgx",
		Roles:            []string{"coding"},
		Health:           health,
		QuotaRemaining:   quota,
		QuotaUnmetered:   false,
		ConcurrencyUsed:  used,
		ConcurrencyLimit: limit,
	}
}

func TestCompanyOpsAssignment_CapacityGateGrantsHealthyCandidate(t *testing.T) {
	backend := newFakeCompanyOpsAssignmentBackend()
	service := NewCompanyOpsAssignmentService(backend)
	req := validCompanyOpsAssignmentRequest()
	req.CapacityCandidate = capacityCandidateFor(req, 1000, "healthy", 0, 4)

	_, err := service.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatalf("dispatch with healthy capacity candidate failed: %v", err)
	}
	if backend.beginCount != 1 {
		t.Fatalf("beginCount = %d, want 1", backend.beginCount)
	}
}

func TestCompanyOpsAssignment_CapacityGateDefersQuotaExhausted(t *testing.T) {
	backend := newFakeCompanyOpsAssignmentBackend()
	service := NewCompanyOpsAssignmentService(backend)
	req := validCompanyOpsAssignmentRequest()
	req.CapacityCandidate = capacityCandidateFor(req, 0, "healthy", 0, 4)

	_, err := service.Dispatch(context.Background(), req)
	if !errors.Is(err, ErrCompanyOpsCapacityDefer) {
		t.Fatalf("err = %v, want ErrCompanyOpsCapacityDefer", err)
	}
	if backend.beginCount != 0 {
		t.Fatalf("beginCount = %d, want 0 (no transaction before capacity gate)", backend.beginCount)
	}
}

func TestCompanyOpsAssignment_CapacityGateRejectsUnhealthy(t *testing.T) {
	backend := newFakeCompanyOpsAssignmentBackend()
	service := NewCompanyOpsAssignmentService(backend)
	req := validCompanyOpsAssignmentRequest()
	req.CapacityCandidate = capacityCandidateFor(req, 1000, "unhealthy", 0, 4)

	_, err := service.Dispatch(context.Background(), req)
	if !errors.Is(err, ErrCompanyOpsCapacityReject) {
		t.Fatalf("err = %v, want ErrCompanyOpsCapacityReject", err)
	}
	if backend.beginCount != 0 {
		t.Fatalf("beginCount = %d, want 0", backend.beginCount)
	}
}

func TestCompanyOpsAssignment_CapacityGateRejectsMismatchedAgent(t *testing.T) {
	backend := newFakeCompanyOpsAssignmentBackend()
	service := NewCompanyOpsAssignmentService(backend)
	req := validCompanyOpsAssignmentRequest()
	candidate := capacityCandidateFor(req, 1000, "healthy", 0, 4)
	candidate.AgentID = "11111111-1111-4111-8111-111111111111"
	req.CapacityCandidate = candidate

	_, err := service.Dispatch(context.Background(), req)
	if !errors.Is(err, ErrCompanyOpsCapacityReject) {
		t.Fatalf("err = %v, want ErrCompanyOpsCapacityReject", err)
	}
	if backend.beginCount != 0 {
		t.Fatalf("beginCount = %d, want 0", backend.beginCount)
	}
}

func TestCompanyOpsAssignment_NoCandidateSkipsGate(t *testing.T) {
	backend := newFakeCompanyOpsAssignmentBackend()
	service := NewCompanyOpsAssignmentService(backend)
	req := validCompanyOpsAssignmentRequest()

	if _, err := service.Dispatch(context.Background(), req); err != nil {
		t.Fatalf("legacy dispatch without capacity candidate failed: %v", err)
	}
	if backend.beginCount != 1 {
		t.Fatalf("beginCount = %d, want 1", backend.beginCount)
	}
}
