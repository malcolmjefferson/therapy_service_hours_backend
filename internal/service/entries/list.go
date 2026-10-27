// Package entries holds the use cases for a user's own session entries.
package entries

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/authz"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// Limits on list input. They match the bounds in api/openapi.yaml (GO-53, SEC-26).
const (
	// MaxClientFilterRunes caps the client text so a filter cannot become a slow scan (SEC-25).
	MaxClientFilterRunes = 64
	// DefaultPageSize is used when the caller does not choose one (GO-56).
	DefaultPageSize = 50
	// MaxPageSize is the largest page a caller may ask for (GO-56).
	MaxPageSize = 200
	// MaxPage bounds the offset so a large page number cannot overflow it.
	MaxPage = 100000
)

// ListRequest is the raw, unvalidated input of a list call. A nil field means "not sent".
type ListRequest struct {
	Group    *string
	Client   *string
	From, To *time.Time
	Sort     *string
	Dir      *string
	Page     *int
	PageSize *int
}

// ListQuery is a validated list request with every default applied. Only the Service builds one.
type ListQuery struct {
	// Group, Client, From and To are optional filters; nil means no filter. Client is trimmed and not
	// yet escaped for LIKE: the store's static SQL does that (SEC-25).
	Group    *domain.CategoryGroup
	Client   *string
	From, To *time.Time
	Sort     domain.EntrySort
	Dir      domain.SortDirection
	Page     int
	PageSize int
}

// Offset is the number of matching rows before the requested page.
func (q ListQuery) Offset() int { return (q.Page - 1) * q.PageSize }

// Store reads and changes entries for the service.
type Store interface {
	Writer

	// ListEntries returns one page of userID's own entries, with their category and clients, in the
	// order and window q describes, together with the total number of entries matching the filters.
	// It never returns another user's entries. A page past the end is empty, not an error.
	ListEntries(ctx context.Context, userID string, q ListQuery) ([]domain.Entry, int64, error)

	// ExportEntries returns one page of everything userID has logged, with category and clients,
	// oldest first (session date, then start time with untimed entries first, then id), together with
	// the total number of entries. It never returns another user's entries. A page past the end is
	// empty, not an error.
	ExportEntries(ctx context.Context, userID string, q ExportQuery) ([]domain.Entry, int64, error)
}

// Service implements the entry use cases.
type Service struct {
	store Store
}

// New returns a Service over store.
func New(store Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("entries service: store is required")
	}
	return &Service{store: store}, nil
}

// Page is one page of entries and where it sits in the full result.
type Page struct {
	Items      []domain.Entry
	Page       int
	PageSize   int
	TotalItems int64
}

// List returns a page of the actor's own entries. It rejects invalid filters, sort or paging with a
// domain.ValidationError that names each field and never echoes the submitted value.
func (s *Service) List(ctx context.Context, actor domain.Actor, req ListRequest) (Page, error) {
	if !authz.Self(actor, authz.Resource{UserID: actor.ID, OrganizationID: actor.OrganizationID}) {
		return Page{}, domain.ErrForbidden
	}
	q, err := validate(req)
	if err != nil {
		return Page{}, err
	}
	items, total, err := s.store.ListEntries(ctx, actor.ID, q)
	if err != nil {
		return Page{}, fmt.Errorf("list entries: %w", err)
	}
	return Page{Items: items, Page: q.Page, PageSize: q.PageSize, TotalItems: total}, nil
}

// validate applies defaults and checks every field, so one response reports all the problems.
func validate(req ListRequest) (ListQuery, error) {
	v := &validator{q: ListQuery{Sort: domain.SortSessionDate, Dir: domain.SortDesc, Page: 1, PageSize: DefaultPageSize, From: req.From, To: req.To}}
	v.group(req.Group)
	v.client(req.Client)
	v.dates(req.From, req.To)
	v.order(req.Sort, req.Dir)
	v.paging(req.Page, req.PageSize)
	if len(v.bad) > 0 {
		return ListQuery{}, domain.NewValidationError(v.bad...)
	}
	return v.q, nil
}

// validator builds a ListQuery field by field and remembers what was wrong.
type validator struct {
	q   ListQuery
	bad []domain.FieldError
}

func (v *validator) fail(field, message string) {
	v.bad = append(v.bad, domain.FieldError{Field: field, Message: message})
}

func (v *validator) group(raw *string) {
	if raw == nil {
		return
	}
	g, ok := domain.ParseCategoryGroup(*raw)
	if !ok {
		v.fail("group", "must be one of direct, indirect, supervision")
		return
	}
	v.q.Group = &g
}

func (v *validator) client(raw *string) {
	if raw == nil {
		return
	}
	text, msg := cleanClientFilter(*raw)
	if msg != "" {
		v.fail("client", msg)
		return
	}
	v.q.Client = &text
}

func (v *validator) dates(from, to *time.Time) {
	if from != nil && to != nil && to.Before(*from) {
		v.fail("to", "must not be before from")
	}
}

func (v *validator) order(sort, dir *string) {
	if sort != nil {
		if c, ok := domain.ParseEntrySort(*sort); ok {
			v.q.Sort = c
		} else {
			v.fail("sort", "must be one of session_date, category, client, hours, on_site")
		}
	}
	if dir != nil {
		if d, ok := domain.ParseSortDirection(*dir); ok {
			v.q.Dir = d
		} else {
			v.fail("dir", "must be asc or desc")
		}
	}
}

func (v *validator) paging(page, size *int) {
	if page != nil {
		if *page < 1 || *page > MaxPage {
			v.fail("page", fmt.Sprintf("must be between 1 and %d", MaxPage))
		} else {
			v.q.Page = *page
		}
	}
	if size != nil {
		if *size < 1 || *size > MaxPageSize {
			v.fail("page_size", fmt.Sprintf("must be between 1 and %d", MaxPageSize))
		} else {
			v.q.PageSize = *size
		}
	}
}

// cleanClientFilter trims the text and returns a message when it cannot be used. Invalid UTF-8 and
// control characters (including NUL) are refused here because PostgreSQL would reject them and the
// caller would see a 500 instead of a 400.
func cleanClientFilter(s string) (text, message string) {
	if !utf8.ValidString(s) {
		return "", "must be valid text"
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", "must not contain control characters"
		}
	}
	text = strings.TrimSpace(s)
	switch n := utf8.RuneCountInString(text); {
	case n == 0:
		return "", "must not be blank"
	case n > MaxClientFilterRunes:
		return "", fmt.Sprintf("must be at most %d characters", MaxClientFilterRunes)
	}
	return text, ""
}
