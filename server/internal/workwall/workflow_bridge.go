package workwall

import (
	"fmt"

	"github.com/multica-ai/multica/server/internal/liveactivity"
	"github.com/multica-ai/multica/server/internal/workflow"
)

// workflowEventSummary maps a workflow event kind to a safe, structured,
// human-readable summary. Raw details never reach the work wall.
func workflowEventSummary(kind string) string {
	switch kind {
	case "workflow.started":
		return "工作流启动"
	case "workflow.stage_advanced":
		return "阶段推进"
	case "workflow.pause":
		return "暂停"
	case "workflow.resume":
		return "恢复"
	case "workflow.stop":
		return "停止"
	case "workflow.fail":
		return "失败"
	case "workflow.recovered":
		return "恢复"
	default:
		return kind
	}
}

// RecentEventFromWorkflow converts a workflow.Event into a sanitized work-wall
// recent event. safe_summary is derived only from the structured event kind;
// no raw workflow content is exposed.
func RecentEventFromWorkflow(ev workflow.Event) liveactivity.RecentEvent {
	return liveactivity.RecentEvent{
		EventID:     fmt.Sprintf("wf-%d", ev.Sequence),
		Kind:        ev.Kind,
		SafeSummary: workflowEventSummary(ev.Kind),
		OccurredAt:  ev.OccurredAt,
		SourceRef:   ev.SourceRef,
	}
}
