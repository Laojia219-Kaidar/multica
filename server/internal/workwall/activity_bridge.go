package workwall

import (
	"sort"

	"github.com/multica-ai/multica/server/internal/liveactivity"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// activityActionSummary maps a controlled activity_log.action value to a safe,
// human-readable summary. Unknown actions fall back to a generic label; raw
// details JSON is never exposed.
func activityActionSummary(action string) string {
	switch action {
	case "task_completed":
		return "任务完成"
	case "task_failed":
		return "任务失败"
	case "issue_created":
		return "议题创建"
	case "issue_updated":
		return "议题更新"
	case "assignee_changed":
		return "负责人变更"
	case "comment_created":
		return "评论"
	case "squad_leader_evaluated":
		return "队长评估"
	default:
		return "活动更新"
	}
}

// RecentEventFromActivity converts one activity_log row into a sanitized
// work-wall recent event. Only the controlled action string is used for the
// summary; details JSON (which may contain raw content) is never read.
func RecentEventFromActivity(a db.ActivityLog) liveactivity.RecentEvent {
	return liveactivity.RecentEvent{
		EventID:     a.ID.String(),
		Kind:        "activity." + a.Action,
		SafeSummary: activityActionSummary(a.Action),
		OccurredAt:  a.CreatedAt.Time,
		SourceRef:   "activity://" + a.ID.String(),
	}
}

// RecentEventsFromActivities maps activity rows to sanitized events, newest
// first, and caps the slice to max.
func RecentEventsFromActivities(rows []db.ActivityLog, max int) []liveactivity.RecentEvent {
	out := make([]liveactivity.RecentEvent, 0, len(rows))
	for i := range rows {
		out = append(out, RecentEventFromActivity(rows[i]))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].OccurredAt.After(out[j].OccurredAt)
	})
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out
}
