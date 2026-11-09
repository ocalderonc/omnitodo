// Package civil holds timezone-free calendar dates and wall-clock times of day.
//
// The planner reasons in local wall-clock terms ("pickup at 12:10 on Monday"),
// so these types deliberately carry no location: the conversion to and from
// time.Time happens once, at the edge, using the configured TZ.
package civil

import (
	"fmt"
	"strings"
	"time"
)

// Date is a calendar day.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// NewDate normalises its arguments, so NewDate(2026, 11, 31) is 2026-12-01.
func NewDate(y int, m time.Month, d int) Date {
	return DateOf(time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
}

// DateOf returns the calendar day of t in t's own location.
func DateOf(t time.Time) Date {
	y, m, d := t.Date()
	return Date{y, m, d}
}

// ParseDate parses YYYY-MM-DD.
func ParseDate(s string) (Date, error) {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return Date{}, fmt.Errorf("invalid date %q: want YYYY-MM-DD", s)
	}
	return DateOf(t), nil
}

func (d Date) t() time.Time { return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC) }

func (d Date) String() string        { return d.t().Format(time.DateOnly) }
func (d Date) IsZero() bool          { return d == Date{} }
func (d Date) Weekday() time.Weekday { return d.t().Weekday() }
func (d Date) AddDays(n int) Date    { return DateOf(d.t().AddDate(0, 0, n)) }
func (d Date) Before(o Date) bool    { return d.t().Before(o.t()) }
func (d Date) After(o Date) bool     { return d.t().After(o.t()) }
func (d Date) In(loc *time.Location) time.Time {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, loc)
}

// Time is a wall-clock time of day in minutes after midnight. Values of 24:00
// and beyond are legal and mean "past midnight of the same planning day",
// which is how an overbooked day is represented.
type Time int

// NewTime builds a Time from hours and minutes.
func NewTime(h, m int) Time { return Time(h*60 + m) }

// ParseTime parses H:MM or HH:MM within 00:00–23:59.
func ParseTime(s string) (Time, error) {
	bad := fmt.Errorf("invalid time %q: want HH:MM", s)
	i := strings.IndexByte(s, ':')
	if i < 1 || i > 2 || len(s)-i != 3 {
		return 0, bad
	}
	h, okH := digits(s[:i])
	m, okM := digits(s[i+1:])
	if !okH || !okM || h > 23 || m > 59 {
		return 0, bad
	}
	return NewTime(h, m), nil
}

func digits(s string) (int, bool) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, s != ""
}

func (t Time) String() string {
	sign := ""
	if t < 0 {
		sign, t = "-", -t
	}
	return fmt.Sprintf("%s%02d:%02d", sign, int(t)/60, int(t)%60)
}

// Add returns t shifted by min minutes.
func (t Time) Add(min int) Time { return t + Time(min) }
