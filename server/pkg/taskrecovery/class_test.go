package taskrecovery

import "testing"

// TestClassifyStructural pins the structural (state-observation) classes that
// win precedence over error-text-derived reasons.
func TestClassifyStructural(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   Signals
		want Class
	}{
		{
			name: "waiting_local_directory status",
			in:   Signals{TaskStatus: "waiting_local_directory", WaitReason: "lock held by another task"},
			want: ClassWaitingLocalDirectory,
		},
		{
			// The structural status wins even when the error text would
			// otherwise classify as a crash.
			name: "waiting status beats crash text",
			in:   Signals{TaskStatus: "waiting_local_directory", ErrorText: "exit status 1"},
			want: ClassWaitingLocalDirectory,
		},
		{
			name: "review task reviewer not configured",
			in:   Signals{TaskKind: "review", ReviewerConfigured: false, ReviewerClaimable: false},
			want: ClassReviewerMissing,
		},
		{
			name: "review task reviewer not claimable",
			in:   Signals{TaskKind: "review", ReviewerConfigured: true, ReviewerClaimable: false},
			want: ClassReviewerMissing,
		},
		{
			// A work task with no reviewer is not a reviewer_missing failure.
			name: "work task ignores reviewer signals",
			in:   Signals{TaskKind: "work", ReviewerConfigured: false, ReviewerClaimable: false},
			want: ClassUnknown,
		},
		{
			name: "dirty worktree",
			in:   Signals{WorktreeDirty: true},
			want: ClassDirtyWorktree,
		},
		{
			name: "dirty worktree beats crash text",
			in:   Signals{WorktreeDirty: true, ErrorText: "panic: runtime error"},
			want: ClassDirtyWorktree,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.in); got != c.want {
				t.Errorf("Classify(%+v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestClassifyReasonDerived pins the error-text / failure_reason derived
// classes.
func TestClassifyReasonDerived(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   Signals
		want Class
	}{
		{
			name: "runtime version unsupported reason",
			in:   Signals{FailureReason: "agent_error.runtime_version_unsupported"},
			want: ClassRuntimeOutdated,
		},
		{
			name: "runtime version below minimum",
			in:   Signals{RuntimeVersion: "0.1.0", MinRuntimeVersion: "0.5.0"},
			want: ClassRuntimeOutdated,
		},
		{
			name: "runtime version text",
			in:   Signals{ErrorText: "claude CLI 0.1.0 is below the minimum supported version 0.5.0"},
			want: ClassRuntimeOutdated,
		},
		{
			name: "runtime version at minimum is not outdated",
			in:   Signals{RuntimeVersion: "0.5.0", MinRuntimeVersion: "0.5.0"},
			want: ClassUnknown,
		},
		{
			name: "quota reason",
			in:   Signals{FailureReason: "agent_error.provider_quota_limit"},
			want: ClassQuotaExhausted,
		},
		{
			name: "quota text",
			in:   Signals{ErrorText: "Your account has 0 credits remaining"},
			want: ClassQuotaExhausted,
		},
		{
			name: "quota 402 boundary",
			in:   Signals{ErrorText: "API Error: 402 Payment Required"},
			want: ClassQuotaExhausted,
		},
		{
			// Digit-boundary guard: 402 embedded in a longer number is not
			// quota; the crash marker still classifies as crash.
			name: "402 embedded not quota",
			in:   Signals{ErrorText: "agent consumed 402913 tokens before exit status 1"},
			want: ClassCrash,
		},
		{
			name: "process failure reason",
			in:   Signals{FailureReason: "agent_error.process_failure"},
			want: ClassCrash,
		},
		{
			name: "runtime recovery reason",
			in:   Signals{FailureReason: "runtime_recovery"},
			want: ClassCrash,
		},
		{
			name: "crash text",
			in:   Signals{ErrorText: "agent terminated by signal: killed"},
			want: ClassCrash,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.in); got != c.want {
				t.Errorf("Classify(%+v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestClassifyMissingReceipt pins the terminal-state receipt check and its
// precedence below the reason-derived classes.
func TestClassifyMissingReceipt(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   Signals
		want Class
	}{
		{
			name: "failed terminal with no receipt",
			in:   Signals{TaskStatus: "failed", ReceiptPresent: false},
			want: ClassMissingReceipt,
		},
		{
			name: "completed terminal with no receipt",
			in:   Signals{TaskStatus: "completed", ReceiptPresent: false},
			want: ClassMissingReceipt,
		},
		{
			// A receipt exists: nothing missing.
			name: "receipt present",
			in:   Signals{TaskStatus: "failed", ReceiptPresent: true},
			want: ClassUnknown,
		},
		{
			// Non-terminal status: no receipt is expected yet.
			name: "running with no receipt",
			in:   Signals{TaskStatus: "running", ReceiptPresent: false},
			want: ClassUnknown,
		},
		{
			// A crash reason wins over the missing-receipt structural check.
			name: "crash reason beats missing receipt",
			in:   Signals{TaskStatus: "failed", ReceiptPresent: false, FailureReason: "agent_error.process_failure"},
			want: ClassCrash,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.in); got != c.want {
				t.Errorf("Classify(%+v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestClassifyUnknown pins the catchall and the empty-signals contract.
func TestClassifyUnknown(t *testing.T) {
	t.Parallel()

	if got := Classify(Signals{}); got != ClassUnknown {
		t.Errorf("Classify(empty) = %q, want %q", got, ClassUnknown)
	}
	if got := Classify(Signals{TaskStatus: "failed", ReceiptPresent: true, ErrorText: "the agent gave up"}); got != ClassUnknown {
		t.Errorf("Classify(unrecognized) = %q, want %q", got, ClassUnknown)
	}
}

// TestAllClassesStable pins the stable ordering of AllClasses.
func TestAllClassesStable(t *testing.T) {
	t.Parallel()

	want := []Class{
		ClassRuntimeOutdated,
		ClassWaitingLocalDirectory,
		ClassQuotaExhausted,
		ClassReviewerMissing,
		ClassDirtyWorktree,
		ClassMissingReceipt,
		ClassCrash,
		ClassUnknown,
	}
	got := AllClasses()
	if len(got) != len(want) {
		t.Fatalf("AllClasses() length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("AllClasses()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
