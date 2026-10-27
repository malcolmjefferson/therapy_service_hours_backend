package entries

import (
	"context"
	"fmt"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/authz"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// ExportRequest is the raw, unvalidated input of an export call. A nil field means "not sent".
type ExportRequest struct {
	Page     *int
	PageSize *int
}

// ExportQuery is a validated export request with the defaults applied. Only the Service builds one.
type ExportQuery struct {
	Page     int
	PageSize int
}

// Offset is the number of entries before the requested page.
func (q ExportQuery) Offset() int { return (q.Page - 1) * q.PageSize }

// Export returns a page of everything the actor has logged, oldest first, with times, clients and
// status (FR-IO-04). It is the actor's own data only: the store takes the actor's id and nothing
// else, so no caller input can name another user. The page size is capped at MaxPageSize; a larger or
// non-positive value is a domain.ValidationError that never echoes the submitted value.
func (s *Service) Export(ctx context.Context, actor domain.Actor, req ExportRequest) (Page, error) {
	if !authz.Self(actor, authz.Resource{UserID: actor.ID, OrganizationID: actor.OrganizationID}) {
		return Page{}, domain.ErrForbidden
	}
	v := &validator{q: ListQuery{Page: 1, PageSize: DefaultPageSize}}
	v.paging(req.Page, req.PageSize)
	if len(v.bad) > 0 {
		return Page{}, domain.NewValidationError(v.bad...)
	}
	q := ExportQuery{Page: v.q.Page, PageSize: v.q.PageSize}
	items, total, err := s.store.ExportEntries(ctx, actor.ID, q)
	if err != nil {
		return Page{}, fmt.Errorf("export entries: %w", err)
	}
	return Page{Items: items, Page: q.Page, PageSize: q.PageSize, TotalItems: total}, nil
}
