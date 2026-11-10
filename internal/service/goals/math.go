package goals

import "math"

// The goal math is pure so it can be tested without storage (S3-04). Entry durations are whole
// minutes (CLAUDE.md §7.1); targets are whole hours (§9).

const minutesPerHour = 60

// Hours converts minutes to hours rounded half up to two decimals, the display precision
// (ASM-06): 75 minutes is 1.25, 20 minutes is 0.33, 50 minutes is 0.83. A negative total, which no
// stored entry can produce, is treated as zero.
func Hours(minutes int64) float64 {
	if minutes <= 0 {
		return 0
	}
	return float64((minutes*100+minutesPerHour/2)/minutesPerHour) / 100
}

// Percent returns how far minutes are toward a target of targetHours, as a whole percent rounded
// **down**, so a milestone (25/50/75/100) is only reported once it has really been reached: 299 of
// 1200 hours' worth of minutes is 24, not 25. It is not capped, so 1300 of 1200 hours is 108 and a
// client can show a goal exceeded. A target that is not positive (no target, as for On-site, or
// invalid data) has no percent and returns 0.
func Percent(minutes int64, targetHours int32) int32 {
	if minutes <= 0 || targetHours <= 0 {
		return 0
	}
	percent := minutes * 100 / (int64(targetHours) * minutesPerHour)
	if percent > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(percent) //nolint:gosec // G115: bounded by the check above.
}
