package db

import (
	"context"
	"fmt"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db/loghoursqueries"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// CategoryStore reads the category lookup table (S2-08). The table is shared reference data, so no
// statement is scoped to a user.
type CategoryStore struct{ database Beginner }

// NewCategoryStore accepts a pool or lazy transaction factory from the composition root.
func NewCategoryStore(database Beginner) *CategoryStore { return &CategoryStore{database: database} }

// List returns every category in display order (sort_order).
func (s *CategoryStore) List(ctx context.Context) ([]domain.Category, error) {
	tx, err := s.database.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin category transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }() // Read only: nothing to commit.
	rows, err := loghoursqueries.New(tx).ListCategories(ctx)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	out := make([]domain.Category, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.Category{ID: row.ID.String(), Name: row.Name, Group: domain.CategoryGroup(row.CategoryGroup)})
	}
	return out, nil
}
