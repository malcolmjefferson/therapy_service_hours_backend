package httpapi

import (
	"context"
	"errors"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/http/gen"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/entries"
)

// errInvalidID means storage returned an ID that is not a UUID, which only a bug or corruption causes.
var errInvalidID = errors.New("stored entry has an invalid ID")

// EntryService is the part of the entries service the handlers use (GO-20). Every method acts on
// the actor's own entries only; a missing entry and someone else's entry are both domain.ErrNotFound.
type EntryService interface {
	// List returns a page of the actor's own entries, or a *domain.ValidationError for bad input.
	List(ctx context.Context, actor domain.Actor, req entries.ListRequest) (entries.Page, error)
	// Get returns one of the actor's entries.
	Get(ctx context.Context, actor domain.Actor, entryID string) (domain.Entry, error)
	// Create stores a new pending entry. It returns a *domain.ValidationError for bad fields and a
	// *domain.EntryOverlapError when the session overlaps another.
	Create(ctx context.Context, actor domain.Actor, in entries.Input) (domain.Entry, error)
	// Update replaces a pending or rejected entry; an approved one is a *domain.EntryLockedError.
	Update(ctx context.Context, actor domain.Actor, entryID string, in entries.Input) (domain.Entry, error)
	// Delete removes a pending or rejected entry; an approved one is a *domain.EntryLockedError.
	Delete(ctx context.Context, actor domain.Actor, entryID string) error
}

// ListEntries returns the caller's own entries. The handler only translates: the service validates
// filters, sort and paging, and the store scopes every query to the actor (GO-10, SEC-10).
func (s *server) ListEntries(ctx context.Context, request gen.ListEntriesRequestObject) (gen.ListEntriesResponseObject, error) {
	page, err := s.entries.List(ctx, ActorFrom(ctx), listRequest(request.Params))
	if err != nil {
		return nil, err //nolint:wrapcheck // WriteError maps domain errors to problems (GO-33).
	}
	items := make([]gen.Entry, 0, len(page.Items))
	for _, e := range page.Items {
		dto, err := entryForOwner(e)
		if err != nil {
			return nil, err
		}
		items = append(items, dto)
	}
	return gen.ListEntries200JSONResponse{
		Items: items,
		Page:  gen.PageInfo{Page: int32(page.Page), PageSize: int32(page.PageSize), TotalItems: page.TotalItems}, //nolint:gosec // G115: bounded by the service (page 1..100000, size 1..200).
	}, nil
}

// GetEntry returns one of the caller's own entries; another user's id is 404, never 403.
func (s *server) GetEntry(ctx context.Context, request gen.GetEntryRequestObject) (gen.GetEntryResponseObject, error) {
	entry, err := s.entries.Get(ctx, ActorFrom(ctx), request.EntryId.String())
	if err != nil {
		return nil, err //nolint:wrapcheck // WriteError maps domain errors to problems (GO-33).
	}
	dto, err := entryForOwner(entry)
	if err != nil {
		return nil, err
	}
	return gen.GetEntry200JSONResponse(dto), nil
}

// CreateEntry logs a new entry. The request schema has no hours, so the body decoder and the runtime
// schema check reject a client-sent value before this runs. The Idempotency-Key header is required
// by the contract; replaying a key is S2-09's middleware.
func (s *server) CreateEntry(ctx context.Context, request gen.CreateEntryRequestObject) (gen.CreateEntryResponseObject, error) {
	if request.Body == nil {
		return nil, errors.New("create entry: body was not decoded")
	}
	entry, err := s.entries.Create(ctx, ActorFrom(ctx), entryInput(*request.Body))
	if err != nil {
		return nil, err //nolint:wrapcheck // WriteError maps domain errors to problems (GO-33).
	}
	dto, err := entryForOwner(entry)
	if err != nil {
		return nil, err
	}
	return gen.CreateEntry201JSONResponse(dto), nil
}

// UpdateEntry replaces one of the caller's pending or rejected entries.
func (s *server) UpdateEntry(ctx context.Context, request gen.UpdateEntryRequestObject) (gen.UpdateEntryResponseObject, error) {
	if request.Body == nil {
		return nil, errors.New("update entry: body was not decoded")
	}
	entry, err := s.entries.Update(ctx, ActorFrom(ctx), request.EntryId.String(), entryInput(*request.Body))
	if err != nil {
		return nil, err //nolint:wrapcheck // WriteError maps domain errors to problems (GO-33).
	}
	dto, err := entryForOwner(entry)
	if err != nil {
		return nil, err
	}
	return gen.UpdateEntry200JSONResponse(dto), nil
}

// DeleteEntry deletes one of the caller's pending or rejected entries.
func (s *server) DeleteEntry(ctx context.Context, request gen.DeleteEntryRequestObject) (gen.DeleteEntryResponseObject, error) {
	if err := s.entries.Delete(ctx, ActorFrom(ctx), request.EntryId.String()); err != nil {
		return nil, err //nolint:wrapcheck // WriteError maps domain errors to problems (GO-33).
	}
	return gen.DeleteEntry204Response{}, nil
}

// entryInput copies a request body into the service's raw input without interpreting it.
func entryInput(body gen.EntryRequest) entries.Input {
	in := entries.Input{
		SessionDate: body.SessionDate.Format(time.DateOnly),
		StartTime:   body.StartTime,
		EndTime:     body.EndTime,
		CategoryID:  body.CategoryId.String(),
	}
	if body.ClientIds != nil {
		in.ClientIDs = make([]string, 0, len(*body.ClientIds))
		for _, id := range *body.ClientIds {
			in.ClientIDs = append(in.ClientIDs, id.String())
		}
	}
	if body.OnSite != nil {
		in.OnSite = *body.OnSite
	}
	if body.Notes != nil {
		in.Notes = *body.Notes
	}
	return in
}

// listRequest copies the query string into the service's raw request without interpreting it.
func listRequest(p gen.ListEntriesParams) entries.ListRequest {
	req := entries.ListRequest{Client: p.Client}
	if p.Group != nil {
		g := string(*p.Group)
		req.Group = &g
	}
	if p.From != nil {
		req.From = &p.From.Time
	}
	if p.To != nil {
		req.To = &p.To.Time
	}
	if p.Sort != nil {
		v := string(*p.Sort)
		req.Sort = &v
	}
	if p.Dir != nil {
		v := string(*p.Dir)
		req.Dir = &v
	}
	if p.Page != nil {
		v := int(*p.Page)
		req.Page = &v
	}
	if p.PageSize != nil {
		v := int(*p.PageSize)
		req.PageSize = &v
	}
	return req
}

// entryForOwner is the explicit owner-facing mapping of an entry (GO-54, ARC-18). Fields are copied
// one by one so a new column on the domain type is never exposed by accident.
func entryForOwner(e domain.Entry) (gen.Entry, error) {
	var id, categoryID openapi_types.UUID
	if err := id.UnmarshalText([]byte(e.ID)); err != nil {
		return gen.Entry{}, errInvalidID
	}
	if err := categoryID.UnmarshalText([]byte(e.Category.ID)); err != nil {
		return gen.Entry{}, errInvalidID
	}
	clients := make([]gen.Client, 0, len(e.Clients))
	for _, c := range e.Clients {
		var clientID openapi_types.UUID
		if err := clientID.UnmarshalText([]byte(c.ID)); err != nil {
			return gen.Entry{}, errInvalidID
		}
		clients = append(clients, gen.Client{Id: clientID, Name: c.Name})
	}
	out := gen.Entry{
		Id:              id,
		SessionDate:     openapi_types.Date{Time: e.SessionDate},
		StartTime:       optionalText(e.StartTime),
		EndTime:         optionalText(e.EndTime),
		DurationMinutes: int32(e.DurationMinutes), //nolint:gosec // G115: a session is at most 24 hours (1440 minutes).
		Hours:           e.Hours(),
		Category:        gen.CategoryRef{Id: categoryID, Name: e.Category.Name, Group: gen.CategoryGroup(e.Category.Group)},
		Clients:         clients,
		OnSite:          e.OnSite,
		Notes:           optionalText(e.Notes),
		Status:          gen.EntryStatus(e.Status),
		NeedsTime:       e.NeedsTime,
		ReviewNote:      optionalText(e.ReviewNote),
		ReviewedAt:      e.ReviewedAt,
		CreatedAt:       e.CreatedAt,
		UpdatedAt:       e.UpdatedAt,
	}
	return out, nil
}

func optionalText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// conflictingEntryDTO is the explicit shape of the session an overlap names (GO-54). It carries no
// notes, clients or category: the caller only needs to find the session in their own table.
func conflictingEntryDTO(c domain.ConflictingEntry) gen.ConflictingEntry {
	var id openapi_types.UUID
	// The store only returns identifiers PostgreSQL produced, so a failure leaves the zero UUID.
	_ = id.UnmarshalText([]byte(c.ID))
	return gen.ConflictingEntry{Id: id, SessionDate: openapi_types.Date{Time: c.SessionDate}, StartTime: c.StartTime, EndTime: c.EndTime}
}
