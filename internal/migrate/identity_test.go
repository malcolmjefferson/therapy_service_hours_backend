package migrate_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/malcolmjefferson/therapy_service_hours_backend/db/migrations"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db/identityqueries"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/migrate"
)

func identityDatabase(t *testing.T) *pgx.Conn {
	t.Helper()
	database := server.NewDatabase(t)
	res := migrate.Run(t.Context(), open(t, database), migrations.FS, migrate.Options{})
	if res.Status != migrate.StatusOK {
		t.Fatalf("migrate identity schema: %+v", res)
	}
	return server.Connect(t, database)
}

func requireSQLState(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Fatalf("expected SQLSTATE %s, got %v", code, err)
	}
}

func TestIdentity_UsersAreUniqueAndLookupsUseTypedBoundQueries(t *testing.T) {
	conn := identityDatabase(t)
	q := identityqueries.New(conn)
	org, err := q.GetDefaultOrganization(t.Context())
	if err != nil || org.Name != "Default Organization" {
		t.Fatalf("default organization: %+v, %v", org, err)
	}
	if _, err := conn.Exec(t.Context(), `INSERT INTO users (organization_id, email, cognito_sub, first_name, is_admin)
	    VALUES ($1, $2, $3, $4, true)`, org.ID, "Pseudonym-A@example.test", "test-sub-a", "Pseudonym A"); err != nil {
		t.Fatal(err)
	}
	user, err := q.GetUserByEmail(t.Context(), "pseudonym-a@EXAMPLE.TEST")
	if err != nil || !user.IsAdmin || user.OrganizationID != org.ID {
		t.Fatalf("case-insensitive lookup: %+v, %v", user, err)
	}
	bySub, err := q.GetUserBySub(t.Context(), pgtype.Text{String: "test-sub-a", Valid: true})
	if err != nil || bySub.ID != user.ID {
		t.Fatalf("subject lookup: %+v, %v", bySub, err)
	}
	for _, sql := range []string{
		`INSERT INTO users (organization_id, email) VALUES ($1, 'PSEUDONYM-A@example.test')`,
		`INSERT INTO users (organization_id, email, cognito_sub) VALUES ($1, 'pseudonym-b@example.test', 'test-sub-a')`,
	} {
		_, err := conn.Exec(t.Context(), sql, org.ID)
		requireSQLState(t, err, "23505")
	}
	for _, email := range []string{"' OR 1=1 --", "'; DROP TABLE users; --"} {
		if _, err := q.GetUserByEmail(t.Context(), email); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("injection-shaped email returned data: %v", err)
		}
	}
	_, err = conn.Exec(t.Context(), `INSERT INTO users (email) VALUES ('no-org@example.test')`)
	requireSQLState(t, err, "23502")
	_, err = conn.Exec(t.Context(), `INSERT INTO users (organization_id, email, status) VALUES ($1, 'bad-status@example.test', 'unknown')`, org.ID)
	requireSQLState(t, err, "23514")
}

func TestIdentity_InvitationLifecycleAndExpiry(t *testing.T) {
	conn := identityDatabase(t)
	q := identityqueries.New(conn)
	org, err := q.GetDefaultOrganization(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), `INSERT INTO invitations (organization_id, email) VALUES ($1, 'pseudonym-a@example.test')`, org.ID); err != nil {
		t.Fatal(err)
	}
	args := identityqueries.GetPendingInvitationByEmailParams{
		Email: "PSEUDONYM-A@EXAMPLE.TEST", At: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}
	invite, err := q.GetPendingInvitationByEmail(t.Context(), args)
	if err != nil || invite.ExpiresAt.Time.Sub(invite.CreatedAt.Time) != 14*24*time.Hour {
		t.Fatalf("pending invitation/default validity: %+v, %v", invite, err)
	}
	_, err = conn.Exec(t.Context(), `INSERT INTO invitations (organization_id, email) VALUES ($1, 'PSEUDONYM-A@example.test')`, org.ID)
	requireSQLState(t, err, "23505")
	args.At.Time = invite.ExpiresAt.Time
	if _, err := q.GetPendingInvitationByEmail(t.Context(), args); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("invitation admitted at expiry: %v", err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE invitations SET status = 'accepted', accepted_at = now() WHERE id = $1`, invite.ID); err != nil {
		t.Fatal(err)
	}
	args.At.Time = invite.CreatedAt.Time
	if _, err := q.GetPendingInvitationByEmail(t.Context(), args); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("accepted invitation admitted: %v", err)
	}
	if _, err := conn.Exec(t.Context(), `INSERT INTO invitations (organization_id, email) VALUES ($1, 'Pseudonym-A@example.test')`, org.ID); err != nil {
		t.Fatalf("re-invite after acceptance: %v", err)
	}
	if _, err := conn.Exec(t.Context(), `UPDATE invitations SET status = 'revoked' WHERE status = 'pending'`); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetPendingInvitationByEmail(t.Context(), args); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("revoked invitation admitted: %v", err)
	}
	_, err = conn.Exec(t.Context(), `INSERT INTO invitations (organization_id, email, roles) VALUES ($1, 'invalid-role@example.test', ARRAY['unknown'])`, org.ID)
	requireSQLState(t, err, "23514")
}
