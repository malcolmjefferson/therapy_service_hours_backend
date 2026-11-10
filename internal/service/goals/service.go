// Package goals is the use case for a user's goal targets and progress (S3-03). Goals are strictly
// private to their owner: every operation requires the active actor to act on themselves, and the
// store scopes every statement to that user, so there is no way to name another user's goal.
package goals

import (
	"context"
	"errors"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/authz"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// MaxTargetHours is the largest target a user can set. The column is a 32-bit integer; this is
// far above any licensure requirement and keeps minute arithmetic small (SEC-26).
const MaxTargetHours int32 = 100000

// Store reads and writes goals. Every method is scoped to userID.
type Store interface {
	// Progress returns the user's goal rows and their minute totals from one snapshot.
	Progress(ctx context.Context, userID string) ([]domain.Goal, domain.ProgressMinutes, error)
	// SetTarget changes the target of one goal and returns the goal. A goal the user does not have
	// (or whose type takes no target) returns domain.ErrNotFound.
	SetTarget(ctx context.Context, userID string, goal domain.GoalType, hours int32) (domain.Goal, error)
}

// HourTotals is an approved and a pending total in hours, to two decimals.
type HourTotals struct{ Approved, Pending float64 }

// Card is one goal with its progress, ready for the response.
type Card struct {
	Type domain.GoalType
	// TargetHours and the percents are nil for On-site, which has no target.
	TargetHours                             *int32
	Hours                                   HourTotals
	ApprovedPercent, PendingPercent         *int32
	LastCelebratedPercent                   int32
	SupervisionGroup, SupervisionIndividual *HourTotals // set only on the supervision goal (FR-GOAL-05)
}

// Service implements the goals use cases.
type Service struct {
	store Store
}

// New returns a Service over store.
func New(store Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("goals service requires a store")
	}
	return &Service{store: store}, nil
}

// Progress returns the actor's four goals in display order with approved and pending progress.
func (s *Service) Progress(ctx context.Context, actor domain.Actor) ([]Card, error) {
	if err := authorize(actor); err != nil {
		return nil, err
	}
	goals, minutes, err := s.store.Progress(ctx, actor.ID)
	if err != nil {
		return nil, err //nolint:wrapcheck // The store already wraps with context.
	}
	byType := make(map[domain.GoalType]domain.Goal, len(goals))
	for _, g := range goals {
		byType[g.Type] = g
	}
	cards := make([]Card, 0, len(domain.GoalTypes))
	for _, t := range domain.GoalTypes {
		g, ok := byType[t]
		if !ok {
			// S3-01 gives every user all four rows; a missing one is corrupt data, not a user error.
			return nil, errors.New("goal progress: stored goals are incomplete")
		}
		cards = append(cards, card(g, minutesFor(t, minutes), minutes))
	}
	return cards, nil
}

// SetTarget changes the actor's target for one goal. A target must be a whole number of hours from
// 1 to MaxTargetHours, and Total Time On-site takes none (FR-GOAL-01, FR-GOAL-02).
func (s *Service) SetTarget(ctx context.Context, actor domain.Actor, goal domain.GoalType, hours int32) (domain.Goal, error) {
	if err := authorize(actor); err != nil {
		return domain.Goal{}, err
	}
	if _, ok := domain.ParseGoalType(string(goal)); !ok {
		return domain.Goal{}, domain.NewValidationError(domain.FieldError{Field: "goal_type", Message: "is not a known goal"})
	}
	var fields []domain.FieldError
	if goal == domain.GoalOnSite {
		fields = append(fields, domain.FieldError{Field: "goal_type", Message: "Total Time On-site is tracked only and has no target"})
	}
	if hours < 1 || hours > MaxTargetHours {
		fields = append(fields, domain.FieldError{Field: "target_hours", Message: "must be a whole number of hours from 1 to 100000"})
	}
	if len(fields) > 0 {
		return domain.Goal{}, domain.NewValidationError(fields...)
	}
	updated, err := s.store.SetTarget(ctx, actor.ID, goal, hours)
	if err != nil {
		return domain.Goal{}, err //nolint:wrapcheck // The store already wraps with context.
	}
	return updated, nil
}

func authorize(actor domain.Actor) error {
	if !authz.Self(actor, authz.Resource{UserID: actor.ID, OrganizationID: actor.OrganizationID}) {
		return domain.ErrForbidden
	}
	return nil
}

func minutesFor(t domain.GoalType, m domain.ProgressMinutes) domain.Minutes {
	switch t {
	case domain.GoalDirect:
		return m.Direct
	case domain.GoalSupervision:
		return m.Supervision
	case domain.GoalTotalActivity:
		return m.TotalActivity
	default:
		return m.OnSite
	}
}

func card(g domain.Goal, m domain.Minutes, all domain.ProgressMinutes) Card {
	c := Card{
		Type:                  g.Type,
		TargetHours:           g.TargetHours,
		Hours:                 totals(m),
		LastCelebratedPercent: g.LastCelebratedPercent,
	}
	if g.TargetHours != nil && *g.TargetHours > 0 {
		approved, pending := Percent(m.Approved, *g.TargetHours), Percent(m.Pending, *g.TargetHours)
		c.ApprovedPercent, c.PendingPercent = &approved, &pending
	}
	if g.Type == domain.GoalSupervision {
		group, individual := totals(all.SupervisionGroup), totals(all.SupervisionIndividual)
		c.SupervisionGroup, c.SupervisionIndividual = &group, &individual
	}
	return c
}

func totals(m domain.Minutes) HourTotals {
	return HourTotals{Approved: Hours(m.Approved), Pending: Hours(m.Pending)}
}
