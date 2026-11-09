// Package recurrence parses and evaluates the minimal v1 recurrence rules:
// "daily", "weekdays" and "weekly:MON,WED". Pure, no I/O.
package recurrence

import (
	"fmt"
	"strings"
	"time"

	"github.com/ocalderonc/omnitodo/internal/civil"
)

type Rule struct {
	form string // "daily", "weekdays" or "weekly"
	days [7]bool
}

var dayNames = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// mondayFirst is the order weekly days are written back in.
var mondayFirst = []time.Weekday{1, 2, 3, 4, 5, 6, 0}

// Parse accepts the rule case-insensitively; String returns its canonical form.
func Parse(s string) (Rule, error) {
	bad := fmt.Errorf("recurrence %q: want daily, weekdays or weekly:MON,WED", s)
	switch v := strings.ToLower(s); v {
	case "daily":
		return Rule{form: v, days: [7]bool{true, true, true, true, true, true, true}}, nil
	case "weekdays":
		return Rule{form: v, days: [7]bool{false, true, true, true, true, true, false}}, nil
	}
	list, ok := strings.CutPrefix(strings.ToLower(s), "weekly:")
	if !ok || list == "" {
		return Rule{}, bad
	}
	r := Rule{form: "weekly"}
	for _, d := range strings.Split(list, ",") {
		wd, ok := dayNames[d]
		if !ok {
			return Rule{}, bad
		}
		r.days[wd] = true
	}
	return r, nil
}

func (r Rule) Matches(d civil.Date) bool { return r.days[d.Weekday()] }

func (r Rule) String() string {
	if r.form != "weekly" {
		return r.form
	}
	var out []string
	for _, wd := range mondayFirst {
		if r.days[wd] {
			out = append(out, strings.ToUpper(wd.String()[:3]))
		}
	}
	return "weekly:" + strings.Join(out, ",")
}
