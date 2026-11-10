package goals_test

import (
	"testing"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/goals"
)

func TestHours(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		minutes int64
		want    float64
	}{{0, 0}, {-5, 0}, {15, 0.25}, {75, 1.25}, {20, 0.33}, {50, 0.83}, {1, 0.02}, {90, 1.5}, {72000, 1200}} {
		if got := goals.Hours(tc.minutes); got != tc.want {
			t.Errorf("Hours(%d) = %v, want %v", tc.minutes, got, tc.want)
		}
	}
}

func TestPercent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		minutes int64
		target  int32
		want    int32
	}{
		{"none", 0, 100, 0},
		{"just below a quarter", 1499, 100, 24},
		{"exactly a quarter", 1500, 100, 25},
		{"half", 3000, 100, 50},
		{"met", 6000, 100, 100},
		{"over target is not capped", 7800, 100, 130},
		{"no target", 600, 0, 0},
		{"negative target", 600, -1, 0},
	} {
		if got := goals.Percent(tc.minutes, tc.target); got != tc.want {
			t.Errorf("%s: Percent(%d, %d) = %d, want %d", tc.name, tc.minutes, tc.target, got, tc.want)
		}
	}
}
