package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db/loghoursqueries"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// GoalStore implements the goals use case's storage (S3-03). Every statement is scoped to the owning
// user in SQL, so another user's goals match no row (GO-47, SEC-12).
type GoalStore struct{ database Beginner }

// NewGoalStore accepts a pool or lazy transaction factory from the composition root.
func NewGoalStore(database Beginner) *GoalStore { return &GoalStore{database: database} }

// Progress returns the user's goal rows and their minute totals. Both reads run in one
// REPEATABLE READ snapshot, so a card's target and hours never come from different moments.
func (s *GoalStore) Progress(ctx context.Context, userID string) ([]domain.Goal, domain.ProgressMinutes, error) {
	owner, err := parseUUID(userID)
	if err != nil {
		return nil, domain.ProgressMinutes{}, fmt.Errorf("goal progress: %w", err)
	}
	tx, err := s.database.Begin(ctx)
	if err != nil {
		return nil, domain.ProgressMinutes{}, fmt.Errorf("begin goal transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }() // Read only: nothing to commit.
	if _, err := tx.Exec(ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY"); err != nil {
		return nil, domain.ProgressMinutes{}, fmt.Errorf("set goal snapshot: %w", err)
	}
	q := loghoursqueries.New(tx)
	rows, err := q.ListGoals(ctx, owner)
	if err != nil {
		return nil, domain.ProgressMinutes{}, fmt.Errorf("list goals: %w", err)
	}
	agg, err := q.GetProgressMinutes(ctx, owner)
	if err != nil {
		return nil, domain.ProgressMinutes{}, fmt.Errorf("get progress minutes: %w", err)
	}
	goals := make([]domain.Goal, 0, len(rows))
	for _, r := range rows {
		goals = append(goals, toGoal(r.GoalType, r.TargetHours, r.LastCelebratedPercent))
	}
	return goals, domain.ProgressMinutes{
		Direct:                domain.Minutes{Approved: agg.DirectApprovedMinutes, Pending: agg.DirectPendingMinutes},
		Supervision:           domain.Minutes{Approved: agg.SupervisionApprovedMinutes, Pending: agg.SupervisionPendingMinutes},
		TotalActivity:         domain.Minutes{Approved: agg.TotalActivityApprovedMinutes, Pending: agg.TotalActivityPendingMinutes},
		OnSite:                domain.Minutes{Approved: agg.OnSiteApprovedMinutes, Pending: agg.OnSitePendingMinutes},
		SupervisionGroup:      domain.Minutes{Approved: agg.SupervisionGroupApprovedMinutes, Pending: agg.SupervisionGroupPendingMinutes},
		SupervisionIndividual: domain.Minutes{Approved: agg.SupervisionIndividualApprovedMinutes, Pending: agg.SupervisionIndividualPendingMinutes},
	}, nil
}

// SetTarget changes one goal's target. On-site, or a goal the user does not have, matches no row
// and returns domain.ErrNotFound.
func (s *GoalStore) SetTarget(ctx context.Context, userID string, goal domain.GoalType, hours int32) (domain.Goal, error) {
	owner, err := parseUUID(userID)
	if err != nil {
		return domain.Goal{}, fmt.Errorf("set goal target: %w", err)
	}
	tx, err := s.database.Begin(ctx)
	if err != nil {
		return domain.Goal{}, fmt.Errorf("begin goal transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	row, err := loghoursqueries.New(tx).UpdateGoalTarget(ctx, loghoursqueries.UpdateGoalTargetParams{
		UserID: owner, GoalType: string(goal), TargetHours: pgtype.Int4{Int32: hours, Valid: true},
	})
	if err != nil {
		return domain.Goal{}, notFoundOr(err, "update goal target")
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Goal{}, fmt.Errorf("commit goal target: %w", err)
	}
	return toGoal(row.GoalType, row.TargetHours, row.LastCelebratedPercent), nil
}

func toGoal(goalType string, target pgtype.Int4, celebrated int16) domain.Goal {
	g := domain.Goal{Type: domain.GoalType(goalType), LastCelebratedPercent: int32(celebrated)}
	if target.Valid {
		t := target.Int32
		g.TargetHours = &t
	}
	return g
}
