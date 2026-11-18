package nextgin

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed canonical 5-field UTC cron expression.
type Schedule struct {
	minutes     [60]bool
	hours       [24]bool
	dom         [32]bool // 1-indexed (1..31)
	months      [13]bool // 1-indexed (1..12)
	dow         [7]bool  // 0..6 where 0 = Sunday
	minCount    int
	hourCount   int
	dowWildcard bool
	src         string
}

// ParseSchedule parses a canonical 5-field cron expression in UTC.
//
// Fields are minute (0-59), hour (0-23), day-of-month (1-31), month (1-12),
// and day-of-week (0-6, 0 = Sunday). Each field is a comma-separated list of
// *, N, N-M, */S, or N-M/S items. Both day-of-month and day-of-week cannot be
// restricted in the same expression.
func ParseSchedule(expr string) (*Schedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("nextgin: invalid cron schedule %q: expected 5 fields, got %d", expr, len(fields))
	}
	if fields[2] != "*" && fields[4] != "*" {
		return nil, fmt.Errorf("nextgin: invalid cron schedule %q: day-of-month and day-of-week cannot both be restricted", expr)
	}

	s := &Schedule{
		dowWildcard: fields[4] == "*",
		src:         strings.Join(fields, " "),
	}

	specs := []struct {
		name string
		raw  string
		min  int
		max  int
		dst  []bool
	}{
		{"minute", fields[0], 0, 59, s.minutes[:]},
		{"hour", fields[1], 0, 23, s.hours[:]},
		{"day-of-month", fields[2], 1, 31, s.dom[:]},
		{"month", fields[3], 1, 12, s.months[:]},
		{"day-of-week", fields[4], 0, 6, s.dow[:]},
	}

	for _, sp := range specs {
		if err := parseCronField(sp.raw, sp.min, sp.max, sp.dst); err != nil {
			return nil, fmt.Errorf("nextgin: invalid cron schedule %q: %s: %w", expr, sp.name, err)
		}
	}

	for _, ok := range s.minutes {
		if ok {
			s.minCount++
		}
	}
	for _, ok := range s.hours {
		if ok {
			s.hourCount++
		}
	}

	return s, nil
}

func parseCronField(field string, minVal, maxVal int, dst []bool) error {
	for _, item := range strings.Split(field, ",") {
		if err := parseCronItem(item, minVal, maxVal, dst); err != nil {
			return err
		}
	}
	return nil
}

func parseCronItem(item string, minVal, maxVal int, dst []bool) error {
	if item == "" {
		return fmt.Errorf("empty item")
	}

	base, stepStr, hasStep := strings.Cut(item, "/")
	step := 1
	if hasStep {
		s, err := parseCronUint(stepStr)
		if err != nil {
			return fmt.Errorf("invalid step in %q: %w", item, err)
		}
		if s < 1 {
			return fmt.Errorf("step must be >= 1 in %q", item)
		}
		step = s
	}

	var start, end int
	if base == "*" {
		start, end = minVal, maxVal
	} else if startStr, endStr, hasRange := strings.Cut(base, "-"); hasRange {
		var err error
		start, err = parseCronUint(startStr)
		if err != nil {
			return fmt.Errorf("invalid range start in %q: %w", item, err)
		}
		end, err = parseCronUint(endStr)
		if err != nil {
			return fmt.Errorf("invalid range end in %q: %w", item, err)
		}
		if start > end {
			return fmt.Errorf("range start > end in %q", item)
		}
		if start < minVal || end > maxVal {
			return fmt.Errorf("value out of range [%d,%d] in %q", minVal, maxVal, item)
		}
	} else {
		if hasStep {
			return fmt.Errorf("step requires * or range in %q", item)
		}
		v, err := parseCronUint(base)
		if err != nil {
			return fmt.Errorf("invalid value in %q: %w", item, err)
		}
		if v < minVal || v > maxVal {
			return fmt.Errorf("value %d out of range [%d,%d]", v, minVal, maxVal)
		}
		start, end = v, v
	}

	for v := start; v <= end; v += step {
		dst[v] = true
	}
	return nil
}

func parseCronUint(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty integer")
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, fmt.Errorf("non-numeric %q", s)
		}
	}
	return strconv.Atoi(s)
}

// String returns the canonical cron expression with whitespace normalized to
// single spaces.
func (s *Schedule) String() string {
	return s.src
}

// Next returns the first matching minute strictly after after (truncated to the
// minute in UTC). The returned time has UTC location with zero seconds and
// nanoseconds. If no time matches within 5 years after after, it returns the
// zero time.Time.
func (s *Schedule) Next(after time.Time) time.Time {
	afterUTC := after.UTC()
	limit := afterUTC.AddDate(5, 0, 0)
	t := afterUTC.Truncate(time.Minute).Add(time.Minute)

	for !t.After(limit) {
		year, month, day := t.Date()
		if !s.months[month] {
			t = time.Date(year, month+1, 1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if !s.dom[day] || !s.dow[t.Weekday()] {
			t = time.Date(year, month, day+1, 0, 0, 0, 0, time.UTC)
			continue
		}
		hour := t.Hour()
		if !s.hours[hour] {
			t = time.Date(year, month, day, hour+1, 0, 0, 0, time.UTC)
			continue
		}
		min := t.Minute()
		if !s.minutes[min] {
			t = time.Date(year, month, day, hour, min+1, 0, 0, time.UTC)
			continue
		}
		return t
	}
	return time.Time{}
}

// RunsPerDayMax returns the maximum number of times the schedule can fire in a
// single day, computed as the number of allowed minutes times the number of
// allowed hours.
func (s *Schedule) RunsPerDayMax() int {
	return s.minCount * s.hourCount
}
