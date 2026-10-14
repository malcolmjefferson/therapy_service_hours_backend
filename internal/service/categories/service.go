// Package categories is the use case for the service-category lookup data (S2-08). The categories
// are the same for everyone, so any active signed-in user may read them.
package categories

import (
	"context"
	"errors"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/authz"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// Store reads the categories, ordered for display (group, then position within the group).
type Store interface {
	List(ctx context.Context) ([]domain.Category, error)
}

// groupOrder is the display order of the groups and their labels (CLAUDE.md §8).
var groupOrder = []struct {
	group domain.CategoryGroup
	label string
}{
	{domain.GroupDirect, "Direct Service"},
	{domain.GroupIndirect, "Indirect Service"},
	{domain.GroupSupervision, "Supervision"},
}

// Service implements the category use case.
type Service struct {
	store Store
}

// New returns a Service over store.
func New(store Store) (*Service, error) {
	if store == nil {
		return nil, errors.New("categories service requires a store")
	}
	return &Service{store: store}, nil
}

// List returns the categories grouped Direct Service, Indirect Service, Supervision, each group in
// the store's order. A group with no categories is left out. A stored category whose group is not
// one of the three is an error rather than silently dropped, because the entry form would then
// offer fewer categories than the database allows.
func (s *Service) List(ctx context.Context, actor domain.Actor) ([]domain.CategorySection, error) {
	if !authz.Self(actor, authz.Resource{UserID: actor.ID, OrganizationID: actor.OrganizationID}) {
		return nil, domain.ErrForbidden
	}
	all, err := s.store.List(ctx)
	if err != nil {
		return nil, err //nolint:wrapcheck // The store already wraps with context.
	}
	byGroup := make(map[domain.CategoryGroup][]domain.Category, len(groupOrder))
	for _, c := range all {
		if _, ok := domain.ParseCategoryGroup(string(c.Group)); !ok {
			return nil, errors.New("list categories: stored category has an unknown group")
		}
		byGroup[c.Group] = append(byGroup[c.Group], c)
	}
	sections := make([]domain.CategorySection, 0, len(groupOrder))
	for _, g := range groupOrder {
		if cats := byGroup[g.group]; len(cats) > 0 {
			sections = append(sections, domain.CategorySection{Group: g.group, Label: g.label, Categories: cats})
		}
	}
	return sections, nil
}
