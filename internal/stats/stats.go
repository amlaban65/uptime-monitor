// Package stats holds the uptime and latency math used by the status page.
// Inputs are plain counts and samples so the functions are easy to test.
package stats

import (
	"math"
	"slices"
	"time"
)

// Uptime returns the percentage of successful checks, or ok=false when there
// is no data. "No data" is deliberately not reported as 100%.
func Uptime(up, total int) (pct float64, ok bool) {
	if total <= 0 {
		return 0, false
	}
	return 100 * float64(up) / float64(total), true
}

// Percentile returns the p-th percentile (0 < p <= 1) of samples using the
// nearest-rank method: the smallest sample such that at least p of all
// samples are less than or equal to it. It always returns a real observed
// value, which is easy to explain. samples is not modified.
func Percentile(samples []time.Duration, p float64) (time.Duration, bool) {
	if len(samples) == 0 || p <= 0 || p > 1 {
		return 0, false
	}
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	rank := int(math.Ceil(p * float64(len(sorted))))
	return sorted[rank-1], true
}

// DayCount is the number of checks and successful checks on one UTC day.
type DayCount struct {
	Day   time.Time // midnight UTC
	Total int
	OK    int
}

// Bar levels for the 90-day uptime strip.
const (
	LevelNoData = "nodata"
	LevelGood   = "good" // >= 99.9%
	LevelWarn   = "warn" // >= 99%
	LevelBad    = "bad"  // < 99%
)

// Bar is one day in the uptime strip.
type Bar struct {
	Day    time.Time `json:"day"`
	Uptime *float64  `json:"uptime"` // nil when there were no checks
	Level  string    `json:"level"`
}

// DailyBars returns exactly n bars ending on today's UTC date, oldest first.
// Days missing from counts are reported as "no data".
func DailyBars(counts []DayCount, today time.Time, n int) []Bar {
	end := truncateDay(today)
	byDay := make(map[time.Time]DayCount, len(counts))
	for _, c := range counts {
		d := truncateDay(c.Day)
		prev := byDay[d]
		byDay[d] = DayCount{Day: d, Total: prev.Total + c.Total, OK: prev.OK + c.OK}
	}
	bars := make([]Bar, n)
	for i := range n {
		day := end.AddDate(0, 0, i-(n-1))
		bar := Bar{Day: day, Level: LevelNoData}
		if c, found := byDay[day]; found {
			if pct, ok := Uptime(c.OK, c.Total); ok {
				bar.Uptime = &pct
				bar.Level = Level(pct)
			}
		}
		bars[i] = bar
	}
	return bars
}

// Level classifies an uptime percentage for display.
func Level(pct float64) string {
	switch {
	case pct >= 99.9:
		return LevelGood
	case pct >= 99:
		return LevelWarn
	default:
		return LevelBad
	}
}

func truncateDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
