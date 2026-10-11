package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db/identityqueries"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/presignup"
)

// PreSignUpReader implements the invitation gate with a single static parameterized query.
type PreSignUpReader struct{ queries *identityqueries.Queries }

// NewPreSignUpReader receives the warmed presignup_ro pool, never the API's app_rw pool.
func NewPreSignUpReader(conn identityqueries.DBTX) *PreSignUpReader {
	return &PreSignUpReader{queries: identityqueries.New(conn)}
}

// Admission reads only users and invitations, without consuming the invitation.
func (r *PreSignUpReader) Admission(ctx context.Context, email string, at time.Time) (presignup.Admission, error) {
	row, err := r.queries.CheckPreSignUpAdmission(ctx, identityqueries.CheckPreSignUpAdmissionParams{
		Email: email, At: pgtype.Timestamptz{Time: at, Valid: true},
	})
	if err != nil {
		return presignup.Admission{}, fmt.Errorf("query pre-sign-up admission: %w", err)
	}
	return presignup.Admission{Invited: row.Invited, Deactivated: row.Deactivated}, nil
}
