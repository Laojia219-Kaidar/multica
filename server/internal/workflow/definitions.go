package workflow

import "time"

// ProjectLifecycleDefinition returns the workflow template for the W2
// HIV-553 project lifecycle (project continuous operation -> review/repair ->
// closure package -> closed). It REUSES the W2 contract; it does not rebuild
// the six closure gates or the five owner actions — those remain W2's sole
// implementation (CROSSWALK D1/D2/D3). This definition only organizes the
// stages so the workflow engine can drive them with exact Task/Run/evidence.
func ProjectLifecycleDefinition() WorkflowDefinition {
	return WorkflowDefinition{
		ID:      "hivecrew.project-lifecycle",
		Version: 1,
		Risk:    RiskStandard, // review/repair and closure require independent review
		Stages: []Stage{
			{Name: "operate", SLA: 7 * 24 * time.Hour},     // ACTIVE: frontier work, stall detection
			{Name: "review_repair", SLA: 48 * time.Hour},   // REVIEW_OR_REPAIR_BLOCKED routing (W2)
			{Name: "closure_pending", SLA: 24 * time.Hour}, // all dispositions complete
			{Name: "close"}, // Closure Package accepted -> CLOSED
		},
	}
}
