package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db/identityqueries"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/seed"
)

// SeedStore bootstraps identities through app_rw; it has no Cognito or DDL privileges.
type SeedStore struct{ pool *pgxpool.Pool }

// NewSeedStore adapts the shared IAM/TLS pool for the operator seed use case.
func NewSeedStore(pool *pgxpool.Pool) *SeedStore { return &SeedStore{pool: pool} }

// VerifyBinding uses a read-only transaction and returns one consistent snapshot.
func (s *SeedStore) VerifyBinding(ctx context.Context, request seed.BindingRequest) (seed.BindingResult, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return seed.BindingResult{}, fmt.Errorf("begin binding verification: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := identityqueries.New(tx).VerifySeedAdminBinding(ctx, identityqueries.VerifySeedAdminBindingParams{Email: request.Email, Subject: request.Subject})
	if err != nil {
		return seed.BindingResult{}, fmt.Errorf("read binding verification: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return seed.BindingResult{}, fmt.Errorf("commit binding verification: %w", err)
	}
	return seed.BindingResult{Users: row.Users, SubjectUsers: row.SubjectUsers, MatchingActiveAdmin: row.MatchingActiveAdmin, AcceptedInvitations: row.AcceptedInvitations}, nil
}

// Apply is serialized and atomic, including expired invitation renewal. Existing bound users,
// deactivation, organization and role assignments are never overwritten by this bootstrap.
func (s *SeedStore) Apply(ctx context.Context, plan seed.Plan) (seed.Result, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return seed.Result{}, fmt.Errorf("begin seed: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := identityqueries.New(tx)
	if err := q.LockIdentitySeed(ctx, 917009); err != nil {
		return seed.Result{}, fmt.Errorf("lock seed: %w", err)
	}
	if err := q.EnsureDefaultOrganization(ctx); err != nil {
		return seed.Result{}, fmt.Errorf("ensure organization: %w", err)
	}
	org, err := q.GetDefaultOrganization(ctx)
	if err != nil {
		return seed.Result{}, fmt.Errorf("read organization: %w", err)
	}
	var result seed.Result
	for _, account := range plan.Accounts {
		count, err := q.InsertSeedUser(ctx, identityqueries.InsertSeedUserParams{
			OrganizationID: org.ID, Email: account.Email, FirstName: account.FirstName, LastName: account.LastName,
			IsAdmin: account.Admin, IsSupervisor: account.Supervisor,
		})
		if err != nil {
			return seed.Result{}, fmt.Errorf("insert seed user: %w", err)
		}
		user, err := q.GetSeedUser(ctx, account.Email)
		if err != nil {
			return seed.Result{}, fmt.Errorf("read seed user: %w", err)
		}
		if user.Status != "active" || user.OrganizationID != org.ID || user.IsAdmin != account.Admin || user.IsSupervisor != account.Supervisor {
			return seed.Result{}, domain.ErrConflict
		}
		result.AccountsCreated += count
		if user.CognitoSub.Valid {
			continue
		} // A repeat run must not re-invite an already bound identity.
		created, renewed, err := seedInvitation(ctx, q, account, org.ID, plan.At)
		if err != nil {
			return seed.Result{}, err
		}
		result.InvitationsCreated += created
		result.InvitationsRenewed += renewed
	}
	if err := tx.Commit(ctx); err != nil {
		return seed.Result{}, fmt.Errorf("commit seed: %w", err)
	}
	return result, nil
}

func seedInvitation(ctx context.Context, q *identityqueries.Queries, account seed.Account, org pgtype.UUID, at time.Time) (int64, int64, error) {
	roles := []string{}
	if account.Admin {
		roles = append(roles, "admin")
	}
	if account.Supervisor {
		roles = append(roles, "supervisor")
	}
	invite, err := q.GetSeedPendingInvitation(ctx, account.Email)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, fmt.Errorf("read seed invitation: %w", err)
	}
	var renewed int64
	if err == nil {
		actualRoles := slices.Clone(invite.Roles)
		slices.Sort(actualRoles)
		if invite.OrganizationID != org || !slices.Equal(actualRoles, roles) {
			return 0, 0, domain.ErrConflict
		}
		if invite.ExpiresAt.Time.After(at) {
			return 0, 0, nil
		}
		renewed, err = q.RevokeExpiredSeedInvitation(ctx, identityqueries.RevokeExpiredSeedInvitationParams{ID: invite.ID, At: pgtype.Timestamptz{Time: at, Valid: true}})
		if err != nil {
			return 0, 0, fmt.Errorf("revoke expired seed invitation: %w", err)
		}
	}
	created, err := q.InsertSeedInvitation(ctx, identityqueries.InsertSeedInvitationParams{
		OrganizationID: org, Email: account.Email, Roles: roles,
		CreatedAt: pgtype.Timestamptz{Time: at, Valid: true}, ExpiresAt: pgtype.Timestamptz{Time: at.Add(14 * 24 * time.Hour), Valid: true},
	})
	if err != nil {
		return 0, 0, fmt.Errorf("insert seed invitation: %w", err)
	}
	if created != 1 {
		return 0, 0, domain.ErrConflict
	} // An external writer changed the invitation; roll back safely.
	return created, renewed, nil
}
