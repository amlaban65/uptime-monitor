package stats

import (
	"math"
	"testing"
	"time"
)

func TestUptime(t *testing.T) {
	tests := []struct {
		up, total int
		want      float64
		wantOK    bool
	}{
		{0, 0, 0, false},
		{10, 10, 100, true},
		{0, 5, 0, true},
		{999, 1000, 99.9, true},
		{1439, 1440, 99.93055555555556, true},
	}
	for _, tt := range tests {
		got, ok := Uptime(tt.up, tt.total)
		if ok != tt.wantOK || math.Abs(got-tt.want) > 1e-9 {
			t.Errorf("Uptime(%d, %d) = %v, %v; want %v, %v", tt.up, tt.total, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestPercentile(t *testing.T) {
	ms := func(vs ...int) []time.Duration {
		out := make([]time.Duration, len(vs))
		for i, v := range vs {
			out[i] = time.Duration(v) * time.Millisecond
		}
		return out
	}
	tests := []struct {
		name    string
		samples []time.Duration
		p       float64
		want    time.Duration
		wantOK  bool
	}{
		{"empty", nil, 0.5, 0, false},
		{"p out of range", ms(1), 0, 0, false},
		{"single sample", ms(42), 0.95, 42 * time.Millisecond, true},
		{"median odd", ms(5, 1, 3), 0.5, 3 * time.Millisecond, true},
		{"median even picks lower", ms(4, 1, 3, 2), 0.5, 2 * time.Millisecond, true},
		{"p95 of 1..100", ms(seq(1, 100)...), 0.95, 95 * time.Millisecond, true},
		{"p95 of 1..20", ms(seq(1, 20)...), 0.95, 19 * time.Millisecond, true},
		{"p100 is max", ms(7, 9, 8), 1, 9 * time.Millisecond, true},
		{"outlier only moves p95 with enough weight", ms(10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 5000), 0.95, 10 * time.Millisecond, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Percentile(tt.samples, tt.p)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("got %v, %v; want %v, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestPercentileDoesNotMutateInput(t *testing.T) {
	in := []time.Duration{3, 1, 2}
	Percentile(in, 0.5)
	if in[0] != 3 || in[1] != 1 || in[2] != 2 {
		t.Fatalf("input was modified: %v", in)
	}
}

func TestDailyBars(t *testing.T) {
	today := time.Date(2026, 3, 10, 15, 30, 0, 0, time.UTC)
	day := func(offset int) time.Time { return time.Date(2026, 3, 10+offset, 0, 0, 0, 0, time.UTC) }
	counts := []DayCount{
		{Day: day(0), Total: 100, OK: 100},
		{Day: day(-1), Total: 1000, OK: 995},
		{Day: day(-2), Total: 1000, OK: 900},
		{Day: day(-200), Total: 1, OK: 1}, // outside the window, ignored
	}
	bars := DailyBars(counts, today, 90)
	if len(bars) != 90 {
		t.Fatalf("len = %d, want 90", len(bars))
	}
	if !bars[89].Day.Equal(day(0)) || !bars[0].Day.Equal(day(-89)) {
		t.Fatalf("range = %v .. %v", bars[0].Day, bars[89].Day)
	}
	wantLevels := map[int]string{89: LevelGood, 88: LevelWarn, 87: LevelBad, 86: LevelNoData, 0: LevelNoData}
	for i, want := range wantLevels {
		if bars[i].Level != want {
			t.Errorf("bar %d level = %s, want %s", i, bars[i].Level, want)
		}
	}
	if bars[86].Uptime != nil {
		t.Errorf("no-data bar has uptime %v", *bars[86].Uptime)
	}
	if got := *bars[88].Uptime; math.Abs(got-99.5) > 1e-9 {
		t.Errorf("bar 88 uptime = %v, want 99.5", got)
	}
}

func TestDailyBarsMergesSameDay(t *testing.T) {
	today := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	bars := DailyBars([]DayCount{
		{Day: today, Total: 10, OK: 10},
		{Day: today.Add(5 * time.Hour), Total: 10, OK: 0},
	}, today, 1)
	if got := *bars[0].Uptime; got != 50 {
		t.Fatalf("uptime = %v, want 50", got)
	}
}

func TestLevelBoundaries(t *testing.T) {
	for pct, want := range map[float64]string{100: LevelGood, 99.9: LevelGood, 99.89: LevelWarn, 99: LevelWarn, 98.99: LevelBad, 0: LevelBad} {
		if got := Level(pct); got != want {
			t.Errorf("Level(%v) = %s, want %s", pct, got, want)
		}
	}
}

func seq(from, to int) []int {
	out := make([]int, 0, to-from+1)
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}
