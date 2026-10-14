package migrate_test

import (
	"testing"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/domain"
)

// S2-08 store test: the real CategoryStore against PostgreSQL with the real migrations (GO-72).

func TestCategoryStore_ListReturnsTheTwelveSeededCategoriesInDisplayOrder(t *testing.T) {
	pool := seedPool(t)
	items, err := db.NewCategoryStore(pool).List(t.Context())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []struct {
		name  string
		group domain.CategoryGroup
	}{
		{"Individual Counseling", domain.GroupDirect},
		{"Group Facilitation", domain.GroupDirect},
		{"Intake Interviews", domain.GroupDirect},
		{"Couples / Family Therapy", domain.GroupDirect},
		{"Psychoeducation", domain.GroupDirect},
		{"Writing Progress Notes / Documentation", domain.GroupIndirect},
		{"Staff Meetings", domain.GroupIndirect},
		{"Case Management", domain.GroupIndirect},
		{"Assessment Scoring", domain.GroupIndirect},
		{"Professional Development", domain.GroupIndirect},
		{"Group Supervision", domain.GroupSupervision},
		{"Individual Supervision", domain.GroupSupervision},
	}
	if len(items) != len(want) {
		t.Fatalf("got %d categories, want %d: %+v", len(items), len(want), items)
	}
	seen := map[string]bool{}
	for i, w := range want {
		got := items[i]
		if got.Name != w.name || got.Group != w.group {
			t.Errorf("category %d = %q/%s, want %q/%s", i, got.Name, got.Group, w.name, w.group)
		}
		if _, ok := domain.ParseCategoryGroup(string(got.Group)); !ok || seen[got.ID] || got.ID == "" {
			t.Errorf("category %d has a bad or repeated id %q", i, got.ID)
		}
		seen[got.ID] = true
	}
}
