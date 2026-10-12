package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db/identityqueries"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// IdentityStore implements transactional first-sign-in binding and current actor reads.
type IdentityStore struct{ database Beginner }

// NewIdentityStore accepts a pool or lazy transaction factory from the composition root.
func NewIdentityStore(database Beginner) *IdentityStore { return &IdentityStore{database: database} }

// Resolve serializes first sign-ins by normalized email; roles/status are never cached.
func (s *IdentityStore) Resolve(ctx context.Context, identity domain.Identity, at time.Time) (domain.Actor, error) {
	tx, err := s.database.Begin(ctx)
	if err != nil {
		return domain.Actor{}, fmt.Errorf("begin identity: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := identityqueries.New(tx)
	if err := q.LockIdentityEmail(ctx, identity.Email); err != nil {
		return domain.Actor{}, fmt.Errorf("lock identity: %w", err)
	}
	sub := pgtype.Text{String: identity.Subject, Valid: true}
	row, err := q.GetActorBySub(ctx, sub)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := bindInvitedIdentity(ctx, q, identity, at); err != nil {
			return domain.Actor{}, err
		}
		row, err = q.GetActorBySub(ctx, sub)
	}
	if err != nil {
		return domain.Actor{}, fmt.Errorf("read actor: %w", err)
	}
	if row.Status != "active" || !strings.EqualFold(row.Email, identity.Email) {
		return domain.Actor{}, domain.ErrForbidden
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Actor{}, fmt.Errorf("commit identity: %w", err)
	}
	return domain.Actor{ID: row.ID.String(), OrganizationID: row.OrganizationID.String(), OrganizationName: row.OrganizationName, Email: row.Email, FirstName: row.FirstName, LastName: row.LastName, Active: true, Admin: row.IsAdmin, Supervisor: row.IsSupervisor}, nil
}

func bindInvitedIdentity(ctx context.Context, q *identityqueries.Queries, identity domain.Identity, at time.Time) error {
	user, err := q.GetIdentityUserByEmail(ctx, identity.Email)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("read identity user: %w", err)
	}
	exists := err == nil
	stamp := pgtype.Timestamptz{Time: at, Valid: true}
	invite, err := q.GetIdentityInvitation(ctx, identityqueries.GetIdentityInvitationParams{Email: identity.Email, At: stamp})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrForbidden
	}
	if err != nil {
		return fmt.Errorf("read identity invitation: %w", err)
	}
	if !exists {
		_, err = q.InsertInvitedUser(ctx, identityqueries.InsertInvitedUserParams{OrganizationID: invite.OrganizationID, Email: identity.Email, FirstName: identity.FirstName, LastName: identity.LastName, IsAdmin: slices.Contains(invite.Roles, "admin"), IsSupervisor: slices.Contains(invite.Roles, "supervisor")})
		if err != nil {
			return fmt.Errorf("provision invited user: %w", err)
		}
		user, err = q.GetIdentityUserByEmail(ctx, identity.Email)
		if err != nil {
			return fmt.Errorf("read provisioned user: %w", err)
		}
	}
	return bindAndAccept(ctx, q, user, invite, identity.Subject, stamp)
}

func bindAndAccept(ctx context.Context, q *identityqueries.Queries, user identityqueries.User, invite identityqueries.Invitation, subject string, stamp pgtype.Timestamptz) error {
	// A concurrent external writer must not change the organization, status or binding.
	if user.Status != "active" || user.CognitoSub.Valid || user.OrganizationID != invite.OrganizationID {
		return domain.ErrForbidden
	}
	bound, err := q.BindIdentitySubject(ctx, identityqueries.BindIdentitySubjectParams{ID: user.ID, CognitoSub: pgtype.Text{String: subject, Valid: true}})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return domain.ErrForbidden
	}
	if err != nil {
		return fmt.Errorf("bind identity: %w", err)
	}
	if bound != 1 {
		return domain.ErrForbidden
	}
	accepted, err := q.AcceptIdentityInvitation(ctx, identityqueries.AcceptIdentityInvitationParams{ID: invite.ID, At: stamp})
	if err != nil {
		return fmt.Errorf("accept identity invitation: %w", err)
	}
	if accepted != 1 {
		return domain.ErrForbidden
	}
	return nil
}
