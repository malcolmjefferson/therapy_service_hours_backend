package httpapi

import (
	"context"
	"errors"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/http/gen"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/entries"
)

// errInvalidID means storage returned an ID that is not a UUID, which only a bug or corruption causes.
var errInvalidID = errors.New("stored entry has an invalid ID")

// EntryLister is the part of the entries service the handlers use (GO-20).
type EntryLister interface {
	// List returns a page of the actor's own entries, or a *domain.ValidationError for bad input.
	List(ctx context.Context, actor domain.Actor, req entries.ListRequest) (entries.Page, error)
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
