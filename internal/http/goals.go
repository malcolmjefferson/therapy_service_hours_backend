package httpapi

import (
	"context"
	"errors"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/http/gen"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/goals"
)

// GoalService is the goals use case the handlers call (S3-03). The service authorizes the actor and
// validates the input; handlers only translate between HTTP and it (GO-10).
type GoalService interface {
	Progress(ctx context.Context, actor domain.Actor) ([]goals.Card, error)
	SetTarget(ctx context.Context, actor domain.Actor, goal domain.GoalType, hours int32) (domain.Goal, error)
}

// GetGoalsProgress returns the caller's four goals with approved and pending progress.
func (s *server) GetGoalsProgress(ctx context.Context, _ gen.GetGoalsProgressRequestObject) (gen.GetGoalsProgressResponseObject, error) {
	cards, err := s.goals.Progress(ctx, ActorFrom(ctx))
	if err != nil {
		return nil, err //nolint:wrapcheck // WriteError maps domain errors (GO-33).
	}
	out := make([]gen.GoalProgress, 0, len(cards))
	for _, c := range cards {
		p := gen.GoalProgress{
			GoalType:              gen.GoalType(c.Type),
			TargetHours:           c.TargetHours,
			ApprovedHours:         c.Hours.Approved,
			PendingHours:          c.Hours.Pending,
			ApprovedPercent:       c.ApprovedPercent,
			PendingPercent:        c.PendingPercent,
			LastCelebratedPercent: c.LastCelebratedPercent,
		}
		if c.SupervisionGroup != nil && c.SupervisionIndividual != nil {
			p.SupervisionSplit = &gen.SupervisionSplit{
				Group:      gen.HoursTotals{ApprovedHours: c.SupervisionGroup.Approved, PendingHours: c.SupervisionGroup.Pending},
				Individual: gen.HoursTotals{ApprovedHours: c.SupervisionIndividual.Approved, PendingHours: c.SupervisionIndividual.Pending},
			}
		}
		out = append(out, p)
	}
	return gen.GetGoalsProgress200JSONResponse{Goals: out}, nil
}

// UpdateGoalTarget sets the target of one of the caller's goals.
func (s *server) UpdateGoalTarget(ctx context.Context, req gen.UpdateGoalTargetRequestObject) (gen.UpdateGoalTargetResponseObject, error) {
	if req.Body == nil {
		return nil, errors.New("update goal target: body was not decoded")
	}
	goal, err := s.goals.SetTarget(ctx, ActorFrom(ctx), domain.GoalType(req.GoalType), req.Body.TargetHours)
	if err != nil {
		return nil, err //nolint:wrapcheck // WriteError maps domain errors (GO-33).
	}
	if goal.TargetHours == nil {
		return nil, errors.New("update goal target: stored goal has no target")
	}
	return gen.UpdateGoalTarget200JSONResponse{GoalType: gen.GoalType(goal.Type), TargetHours: *goal.TargetHours}, nil
}
