package nextgin

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseScheduleValid(t *testing.T) {
	cases := []struct {
		name string
		expr string
		want string
	}{
		{name: "every minute", expr: "* * * * *", want: "* * * * *"},
		{name: "whitespace normalized", expr: "  */5   *  * * *  ", want: "*/5 * * * *"},
		{name: "tabs and spaces", expr: "\t0\t9\t*\t*\t1-5\n", want: "0 9 * * 1-5"},
		{name: "ranges and steps", expr: "0-30/10 8-18/2 1,15,28-31/2 1-6,12 *", want: "0-30/10 8-18/2 1,15,28-31/2 1-6,12 *"},
		{name: "sunday zero", expr: "0 3 * * 0", want: "0 3 * * 0"},
		{name: "leading zeros", expr: "00 09 01 01 *", want: "00 09 01 01 *"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := ParseSchedule(tc.expr)
			if err != nil {
				t.Fatalf("ParseSchedule(%q) unexpected error: %v", tc.expr, err)
			}
			if got := s.String(); got != tc.want {
				t.Fatalf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseScheduleRejects(t *testing.T) {
	cases := []struct {
		name string
		expr string
	}{
		{name: "empty", expr: ""},
		{name: "too few fields", expr: "* * * *"},
		{name: "too many fields (6-field)", expr: "0 * * * * *"},
		{name: "weekday name MON", expr: "0 0 * * MON"},
		{name: "month name JAN", expr: "0 0 * JAN *"},
		{name: "question mark", expr: "0 0 ? * *"},
		{name: "last day L", expr: "0 0 L * *"},
		{name: "weekday W", expr: "0 0 15W * *"},
		{name: "nth weekday #", expr: "0 0 * * 1#1"},
		{name: "macro @hourly", expr: "@hourly"},
		{name: "empty item middle", expr: "1,,2 * * * *"},
		{name: "empty item leading", expr: ",1 * * * *"},
		{name: "empty item trailing", expr: "1, * * * *"},
		{name: "minute out of range", expr: "60 * * * *"},
		{name: "hour out of range", expr: "* 24 * * *"},
		{name: "dom zero", expr: "* * 0 * *"},
		{name: "dom out of range", expr: "* * 32 * *"},
		{name: "month zero", expr: "* * * 0 *"},
		{name: "month out of range", expr: "* * * 13 *"},
		{name: "dow 7 out of range", expr: "* * * * 7"},
		{name: "range N > M", expr: "10-5 * * * *"},
		{name: "range step N > M", expr: "10-5/2 * * * *"},
		{name: "step zero wildcard", expr: "*/0 * * * *"},
		{name: "step zero range", expr: "1-10/0 * * * *"},
		{name: "non-numeric", expr: "abc * * * *"},
		{name: "plus sign", expr: "+1 * * * *"},
		{name: "negative", expr: "-1 * * * *"},
		{name: "single value with step", expr: "5/2 * * * *"},
		{name: "double hyphen", expr: "1-2-3 * * * *"},
		{name: "double slash", expr: "*/2/3 * * * *"},
		{name: "both dom and dow restricted", expr: "0 0 1 * 1"},
		{name: "both dom and dow ranges", expr: "0 0 1-5 * 0-6"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := ParseSchedule(tc.expr)
			if err == nil {
				t.Fatalf("ParseSchedule(%q) succeeded (%v), want error", tc.expr, s)
			}
			const prefix = "nextgin: invalid cron schedule"
			if !strings.HasPrefix(err.Error(), prefix) {
				t.Fatalf("error %q does not start with %q", err.Error(), prefix)
			}
			quoted := fmt.Sprintf("%q", tc.expr)
			if !strings.Contains(err.Error(), quoted) {
				t.Fatalf("error %q does not quote expression %s", err.Error(), quoted)
			}
		})
	}
}

func TestScheduleNext(t *testing.T) {
	fixedPlus1 := time.FixedZone("x", 3600)

	cases := []struct {
		name  string
		expr  string
		after time.Time
		want  time.Time
	}{
		{
			name:  "every 15m mid-interval",
			expr:  "*/15 * * * *",
			after: time.Date(2026, 1, 1, 10, 7, 30, 0, time.UTC),
			want:  time.Date(2026, 1, 1, 10, 15, 0, 0, time.UTC),
		},
		{
			name:  "every 15m exact match is strictly after",
			expr:  "*/15 * * * *",
			after: time.Date(2026, 1, 1, 10, 15, 0, 0, time.UTC),
			want:  time.Date(2026, 1, 1, 10, 30, 0, 0, time.UTC),
		},
		{
			name:  "monday 03:00 on monday 03:00 advances one week",
			expr:  "0 3 * * 1",
			after: time.Date(2026, 11, 16, 3, 0, 0, 0, time.UTC),
			want:  time.Date(2026, 11, 23, 3, 0, 0, 0, time.UTC),
		},
		{
			name:  "year end Dec 31 23:30",
			expr:  "30 23 31 12 *",
			after: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			want:  time.Date(2026, 12, 31, 23, 30, 0, 0, time.UTC),
		},
		{
			name:  "leap day Feb 29",
			expr:  "0 0 29 2 *",
			after: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			want:  time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC),
		},
		{
			name:  "impossible Feb 31 returns zero time",
			expr:  "0 0 31 2 *",
			after: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			want:  time.Time{},
		},
		{
			name:  "non-UTC converted to UTC first",
			expr:  "*/15 * * * *",
			after: time.Date(2026, 1, 1, 11, 7, 30, 500, fixedPlus1), // 10:07:30.000000500Z
			want:  time.Date(2026, 1, 1, 10, 15, 0, 0, time.UTC),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := ParseSchedule(tc.expr)
			if err != nil {
				t.Fatalf("ParseSchedule(%q): %v", tc.expr, err)
			}
			start := time.Now()
			got := s.Next(tc.after)
			elapsed := time.Since(start)
			if elapsed > 50*time.Millisecond {
				t.Fatalf("Next() took %s, expected fast skip", elapsed)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("Next(%s) = %s, want %s", tc.after, got, tc.want)
			}
			if !tc.want.IsZero() {
				if got.Location() != time.UTC {
					t.Fatalf("Next() location = %v, want UTC", got.Location())
				}
				if got.Second() != 0 || got.Nanosecond() != 0 {
					t.Fatalf("Next() sec/nsec = %d/%d, want 0/0", got.Second(), got.Nanosecond())
				}
			}
		})
	}
}

func TestScheduleRunsPerDayMax(t *testing.T) {
	cases := []struct {
		expr string
		want int
	}{
		{expr: "*/5 * * * *", want: 288},
		{expr: "0 3 * * *", want: 1},
		{expr: "0,30 9-17 * * *", want: 18},
		{expr: "0,0-5/5 * * * *", want: 48},
	}

	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			s, err := ParseSchedule(tc.expr)
			if err != nil {
				t.Fatalf("ParseSchedule(%q): %v", tc.expr, err)
			}
			if got := s.RunsPerDayMax(); got != tc.want {
				t.Fatalf("RunsPerDayMax() = %d, want %d", got, tc.want)
			}
		})
	}
}
