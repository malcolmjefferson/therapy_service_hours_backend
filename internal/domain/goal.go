package domain

// GoalType is one of a user's four goals (CLAUDE.md §7.2). The values are the API and database names.
type GoalType string

// The four goals, in display order.
const (
	GoalDirect        GoalType = "direct"
	GoalSupervision   GoalType = "supervision"
	GoalTotalActivity GoalType = "total_activity"
	GoalOnSite        GoalType = "on_site"
)

// GoalTypes lists the goals in the order the goal cards are shown.
var GoalTypes = []GoalType{GoalDirect, GoalSupervision, GoalTotalActivity, GoalOnSite}

// ParseGoalType returns the goal named s, or false when s is not one of the four.
func ParseGoalType(s string) (GoalType, bool) {
	switch g := GoalType(s); g {
	case GoalDirect, GoalSupervision, GoalTotalActivity, GoalOnSite:
		return g, true
	default:
		return "", false
	}
}

// Goal is one stored goal row: the editable target and the highest milestone already celebrated.
type Goal struct {
	Type GoalType
	// TargetHours is whole hours; nil only for GoalOnSite, which is tracked and has no target.
	TargetHours           *int32
	LastCelebratedPercent int32
}

// Minutes is an approved and a pending total in minutes. Rejected entries are never counted (FR-GOAL-03).
type Minutes struct {
	Approved, Pending int64
}

// ProgressMinutes is the minute totals behind every goal card, from one read of the user's entries.
type ProgressMinutes struct {
	Direct, Supervision, TotalActivity, OnSite Minutes
	// SupervisionGroup and SupervisionIndividual split Supervision for its card (FR-GOAL-05).
	SupervisionGroup, SupervisionIndividual Minutes
}
