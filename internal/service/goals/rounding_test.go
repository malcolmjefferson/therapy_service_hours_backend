package goals_test

import (
	"fmt"
	"testing"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/db/loghoursqueries"
	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/service/goals"
)

// Rounding rules under test (S3-04, FR-GOAL-03, ASM-06):
//   - Hours rounds half up to two decimals.
//   - Percent rounds DOWN to a whole percent and is not capped, so a milestone is only reported once reached.

func TestHours_RoundsHalfUpToTwoDecimals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		minutes int64
		want    float64
	}{
		{0, 0},
		{1, 0.02},   // 0.0166 rounds up
		{14, 0.23},  // 0.2333 rounds down
		{15, 0.25},  // exact
		{20, 0.33},  // 0.3333 rounds down
		{40, 0.67},  // 0.6666 rounds up
		{50, 0.83},  // 0.8333 rounds down
		{59, 0.98},  // 0.9833 rounds up
		{60, 1},     // one hour
		{61, 1.02},  // 1.0166 rounds up
		{75, 1.25},  // exact quarter hours
		{105, 1.75}, // exact
		{-1, 0},     // no stored entry is negative; treated as zero
		{6000, 100}, // a whole target
		{72000, 1200},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%dmin", tc.minutes), func(t *testing.T) {
			t.Parallel()
			if got := goals.Hours(tc.minutes); got != tc.want {
				t.Errorf("Hours(%d) = %v, want %v", tc.minutes, got, tc.want)
			}
		})
	}
}

func TestPercent_MilestoneBoundariesRoundDown(t *testing.T) {
	t.Parallel()
	const target = 100 // hours; 6000 minutes is 100%
	tests := []struct {
		name    string
		minutes int64
		want    int32
	}{
		{"zero entries", 0, 0},
		{"one minute", 1, 0},
		{"one minute under 1 percent", 59, 0},
		{"exactly 1 percent", 60, 1},
		{"one minute under a quarter", 1499, 24},
		{"exactly a quarter", 1500, 25},
		{"one minute over a quarter", 1501, 25},
		{"one minute under half", 2999, 49},
		{"exactly half", 3000, 50},
		{"one minute under three quarters", 4499, 74},
		{"exactly three quarters", 4500, 75},
		{"one minute under the target", 5999, 99},
		{"exactly the target", 6000, 100},
		{"one minute over the target stays 100", 6001, 100},
		{"a minute short of double", 11999, 199},
		{"double the target", 12000, 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := goals.Percent(tc.minutes, target); got != tc.want {
				t.Errorf("Percent(%d, %d) = %d, want %d", tc.minutes, target, got, tc.want)
			}
		})
	}
}

func TestPercent_TargetChangeRescalesTheSameMinutes(t *testing.T) {
	t.Parallel()
	const minutes = 60 * 300 // 300 approved hours
	tests := []struct {
		name   string
		target int32
		want   int32
	}{
		{"default direct target", 1200, 25},
		{"lowered target", 600, 50},
		{"target equal to progress", 300, 100},
		{"target below progress is over 100", 200, 150},
		{"raised target", 3000, 10},
		{"smallest target", 1, 30000},
		{"no target", 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := goals.Percent(minutes, tc.target); got != tc.want {
				t.Errorf("Percent(%d, %d) = %d, want %d", int64(minutes), tc.target, got, tc.want)
			}
		})
	}
}

func TestPercent_NeverOverflowsAtLargeValues(t *testing.T) {
	t.Parallel()
	// Largest whole-hour target against a very large total must not wrap around.
	const maxTarget = int32(1<<31 - 1)
	if got := goals.Percent(int64(maxTarget)*60, maxTarget); got != 100 {
		t.Errorf("Percent at max target = %d, want 100", got)
	}
	if got := goals.Percent(1<<40, 1200); got <= 0 {
		t.Errorf("Percent of a huge total = %d, want positive", got)
	}
}

func TestPercent_IsMonotonicInMinutes(t *testing.T) {
	t.Parallel()
	for _, target := range []int32{1, 7, 100, 1200, 3000} {
		var prev int32
		for m := int64(0); m <= int64(target)*60*2; m += 15 {
			got := goals.Percent(m, target)
			if got < prev {
				t.Fatalf("Percent(%d, %d) = %d fell below %d", m, target, got, prev)
			}
			prev = got
		}
	}
}

// approvedFor maps each goal to the approved total behind it from the S3-02 aggregate row, the way
// the progress service does. It is test-local on purpose: it pins the meaning of the row's columns.
func approvedFor(row loghoursqueries.GetProgressMinutesRow) map[string]int64 {
	return map[string]int64{
		"direct":         row.DirectApprovedMinutes,
		"supervision":    row.SupervisionApprovedMinutes,
		"total_activity": row.TotalActivityApprovedMinutes,
		"on_site":        row.OnSiteApprovedMinutes,
	}
}

func TestProgressRow_PercentPerGoal(t *testing.T) {
	t.Parallel()
	targets := map[string]int32{"direct": 1200, "supervision": 100, "total_activity": 3000, "on_site": 0}
	tests := []struct {
		name string
		row  loghoursqueries.GetProgressMinutesRow
		want map[string]int32
	}{
		{
			name: "zero entries",
			row:  loghoursqueries.GetProgressMinutesRow{},
			want: map[string]int32{"direct": 0, "supervision": 0, "total_activity": 0, "on_site": 0},
		},
		{
			name: "pending hours do not move the bar",
			row: loghoursqueries.GetProgressMinutesRow{
				DirectPendingMinutes: 60 * 600, SupervisionPendingMinutes: 60 * 50, TotalActivityPendingMinutes: 60 * 650,
			},
			want: map[string]int32{"direct": 0, "supervision": 0, "total_activity": 0, "on_site": 0},
		},
		{
			name: "supervision combines group and individual",
			row: loghoursqueries.GetProgressMinutesRow{
				SupervisionApprovedMinutes:           60 * 50,
				SupervisionGroupApprovedMinutes:      60 * 20,
				SupervisionIndividualApprovedMinutes: 60 * 30,
				TotalActivityApprovedMinutes:         60 * 50,
			},
			want: map[string]int32{"direct": 0, "supervision": 50, "total_activity": 1, "on_site": 0},
		},
		{
			name: "over 100 percent is not capped; on-site has no target",
			row: loghoursqueries.GetProgressMinutesRow{
				DirectApprovedMinutes: 60 * 1500, SupervisionApprovedMinutes: 60 * 130,
				TotalActivityApprovedMinutes: 60 * 3000, OnSiteApprovedMinutes: 60 * 400,
			},
			want: map[string]int32{"direct": 125, "supervision": 130, "total_activity": 100, "on_site": 0},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for goal, minutes := range approvedFor(tc.row) {
				if got := goals.Percent(minutes, targets[goal]); got != tc.want[goal] {
					t.Errorf("%s: Percent(%d, %d) = %d, want %d", goal, minutes, targets[goal], got, tc.want[goal])
				}
			}
		})
	}
}

func TestProgressRow_SupervisionSplitSumsToCombined(t *testing.T) {
	t.Parallel()
	row := loghoursqueries.GetProgressMinutesRow{
		SupervisionApprovedMinutes:           90 + 45,
		SupervisionGroupApprovedMinutes:      90,
		SupervisionIndividualApprovedMinutes: 45,
	}
	if row.SupervisionGroupApprovedMinutes+row.SupervisionIndividualApprovedMinutes != row.SupervisionApprovedMinutes {
		t.Fatal("fixture: split must sum to the combined total")
	}
	if got, want := goals.Hours(row.SupervisionApprovedMinutes), 2.25; got != want {
		t.Errorf("Hours(combined) = %v, want %v", got, want)
	}
}
