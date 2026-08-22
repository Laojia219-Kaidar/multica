package main

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/scheduler"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type recordingSchedulerRegistry struct {
	specs []scheduler.JobSpec
	err   error
}

func (r *recordingSchedulerRegistry) Register(spec scheduler.JobSpec) error {
	if r.err != nil {
		return r.err
	}
	r.specs = append(r.specs, spec)
	return nil
}

func (r *recordingSchedulerRegistry) find(name string) *scheduler.JobSpec {
	for i := range r.specs {
		if r.specs[i].Name == name {
			return &r.specs[i]
		}
	}
	return nil
}

type registrationOwnerReader struct{}

func (registrationOwnerReader) ListMembers(context.Context, pgtype.UUID) ([]db.Member, error) {
	return nil, nil
}

// Registration is default-off: without the exact env value nothing is
// registered, so the automatic drain can never appear as a surprise writer.
func TestWorkConservingDrainRegistration_DefaultOff(t *testing.T) {
	registry := &recordingSchedulerRegistry{}
	drain := service.NewWorkConservingDrainService(nil, nil)
	for _, autoDrain := range []string{"", "0", "off", "false"} {
		if registerWorkConservingDrainJob(registry, drain, registrationOwnerReader{}, "/goal/CHECKLIST.yaml", autoDrain) {
			t.Fatalf("autoDrain %q must not enable registration", autoDrain)
		}
	}
	if registry.find("work_conserving_drain") != nil {
		t.Fatal("work_conserving_drain must be absent by default")
	}
}

// Only the exact value "true" enables the job. Loose truthiness ("1", "TRUE",
// "yes") must stay off so no deployment environment quirk can turn on a job
// that writes Tasks.
func TestWorkConservingDrainRegistration_OnlyExactTrueEnables(t *testing.T) {
	for _, autoDrain := range []string{"1", "TRUE", "True", "yes", " false", "true ", "true\n", "y"} {
		t.Run("disabled_"+autoDrain, func(t *testing.T) {
			registry := &recordingSchedulerRegistry{}
			if registerWorkConservingDrainJob(registry, service.NewWorkConservingDrainService(nil, nil), registrationOwnerReader{}, "/goal/CHECKLIST.yaml", autoDrain) {
				t.Fatalf("autoDrain %q must not enable registration", autoDrain)
			}
			if registry.find("work_conserving_drain") != nil {
				t.Fatalf("autoDrain %q registered the job", autoDrain)
			}
		})
	}

	registry := &recordingSchedulerRegistry{}
	if !registerWorkConservingDrainJob(registry, service.NewWorkConservingDrainService(nil, nil), registrationOwnerReader{}, "/goal/CHECKLIST.yaml", "true") {
		t.Fatal(`only the exact value "true" must enable registration`)
	}
	spec := registry.find("work_conserving_drain")
	if spec == nil {
		t.Fatal("work_conserving_drain must be registered for the exact value true")
	}
	if spec.AllowStaleReentry || spec.MaxAttempts != 1 || len(spec.RetryBackoff) != 0 {
		t.Fatalf("registered spec = %+v, want non-reentrant one-attempt job", spec)
	}
}

// A nil drain (projection or dispatch writer unavailable) must never register:
// there is no job to run, not a job that fails every tick.
func TestWorkConservingDrainRegistration_NilDrainNeverRegisters(t *testing.T) {
	registry := &recordingSchedulerRegistry{}
	if registerWorkConservingDrainJob(registry, nil, registrationOwnerReader{}, "/goal/CHECKLIST.yaml", "true") {
		t.Fatal("nil drain must never register")
	}
	if len(registry.specs) != 0 {
		t.Fatalf("specs = %d, want zero", len(registry.specs))
	}
}

// A nil registry (scheduler not constructed) is a safe no-op, and a failed
// registration is reported as not-enabled instead of panicking.
func TestWorkConservingDrainRegistration_NilRegistryAndFailedRegistrationAreSafe(t *testing.T) {
	if registerWorkConservingDrainJob(nil, service.NewWorkConservingDrainService(nil, nil), registrationOwnerReader{}, "/goal/CHECKLIST.yaml", "true") {
		t.Fatal("nil registry must be a no-op")
	}
	failing := &recordingSchedulerRegistry{err: errors.New("duplicate job name")}
	if registerWorkConservingDrainJob(failing, service.NewWorkConservingDrainService(nil, nil), registrationOwnerReader{}, "/goal/CHECKLIST.yaml", "true") {
		t.Fatal("failed registration must report not-enabled")
	}
}
