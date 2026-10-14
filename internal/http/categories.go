package httpapi

import (
	"context"
	"errors"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/http/gen"
)

// categoriesCacheControl lets the signed-in user's browser keep the lookup list for an hour. The
// list is the same for everyone and changes only with a migration, so this is the one API response
// that is not no-store; `private` keeps shared caches out (ADR-0012, an exception to SEC-33).
const categoriesCacheControl = "private, max-age=3600"

// CategoryService is the categories use case the handlers call (S2-08).
type CategoryService interface {
	// List returns the categories grouped in display order.
	List(ctx context.Context, actor domain.Actor) ([]domain.CategorySection, error)
}

// ListCategories returns the grouped category lookup data.
func (s *server) ListCategories(ctx context.Context, _ gen.ListCategoriesRequestObject) (gen.ListCategoriesResponseObject, error) {
	sections, err := s.categories.List(ctx, ActorFrom(ctx))
	if err != nil {
		return nil, err //nolint:wrapcheck // WriteError maps domain errors (GO-33).
	}
	groups := make([]gen.CategoryGroupSection, 0, len(sections))
	for _, section := range sections {
		items := make([]gen.Category, 0, len(section.Categories))
		for _, c := range section.Categories {
			var id openapi_types.UUID
			if err := id.UnmarshalText([]byte(c.ID)); err != nil {
				return nil, errors.New("stored category has an invalid ID")
			}
			items = append(items, gen.Category{Id: id, Name: c.Name})
		}
		groups = append(groups, gen.CategoryGroupSection{Group: gen.CategoryGroup(section.Group), Label: section.Label, Categories: items})
	}
	cacheControl := categoriesCacheControl
	return gen.ListCategories200JSONResponse{
		Body:    gen.CategoryListResponse{Groups: groups},
		Headers: gen.ListCategories200ResponseHeaders{CacheControl: &cacheControl},
	}, nil
}
