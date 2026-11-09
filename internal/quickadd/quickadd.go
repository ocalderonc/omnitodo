// Package quickadd parses the single-line quick-add syntax:
//
//	Fix LG webhook retry 1h30m +LG #backend @14:00 !2026-11-12
//	Kindergarten pickup 40m ~d @12:10 *weekdays
//
// It is pure: "today" is passed in so relative due dates are deterministic.
// Project and tag names are returned as typed; resolving them (case-
// insensitive match, creating missing tags) is the caller's job.
package quickadd

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ocalderonc/omnitodo/internal/civil"
)

type Result struct {
	Title       string
	DurationMin int
	Distractor  bool
	Project     string      // "" if none
	Tags        []string    // in order, de-duplicated case-insensitively
	StartAt     *civil.Time // nil = floating
	DueDate     *civil.Date
	Recurrence  string // "", "daily", "weekdays" or "weekly:MON,WED"
	// DefaultedDuration is true when the line had no duration and DurationMin
	// was set to DefaultDurationMin, so the UI can say so.
	DefaultedDuration bool
}

// DefaultDurationMin is used when a quick-add line has no duration.
const DefaultDurationMin = 30

var ErrMissingTitle = errors.New("missing title")

var durationRe = regexp.MustCompile(`^(?:(\d{1,2})h)?(?:(\d{1,4})m)?$`)

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// Parse reads one quick-add line. today anchors relative due dates.
func Parse(input string, today civil.Date) (Result, error) {
	var r Result
	var title []string
	durationIdx := -1 // index in title of the duration token currently chosen
	seenTags := map[string]bool{}

	for _, tok := range strings.Fields(input) {
		if d, ok := parseDuration(tok); ok {
			// The last duration wins; an earlier one goes back into the title,
			// so "Read 2 chapters 1h" and "Plan Q2 2h 30m" behave.
			if durationIdx >= 0 {
				title[durationIdx] = strings.TrimPrefix(title[durationIdx], "\x00")
			}
			r.DurationMin = d
			durationIdx = len(title)
			title = append(title, "\x00"+tok) // placeholder, dropped below unless demoted
			continue
		}
		if len(tok) < 2 {
			title = append(title, tok)
			continue
		}
		val := tok[1:]
		switch tok[0] {
		case '+':
			if r.Project != "" {
				return Result{}, fmt.Errorf("two projects: +%s and %s", r.Project, tok)
			}
			r.Project = val
		case '#':
			if k := strings.ToLower(val); !seenTags[k] {
				seenTags[k] = true
				r.Tags = append(r.Tags, val)
			}
		case '@':
			t, err := civil.ParseTime(val)
			if err != nil {
				return Result{}, fmt.Errorf("start time %s: want @HH:MM", tok)
			}
			r.StartAt = &t
		case '!':
			d, err := parseDue(strings.ToLower(val), today)
			if err != nil {
				return Result{}, err
			}
			r.DueDate = &d
		case '~':
			if strings.ToLower(val) != "d" {
				return Result{}, fmt.Errorf("unknown kind %s: only ~d (distractor) exists", tok)
			}
			r.Distractor = true
		case '*':
			rec, err := parseRecurrence(val)
			if err != nil {
				return Result{}, err
			}
			r.Recurrence = rec
		default:
			title = append(title, tok)
		}
	}

	words := title[:0]
	for _, w := range title {
		if !strings.HasPrefix(w, "\x00") {
			words = append(words, w)
		}
	}
	r.Title = strings.Join(words, " ")
	if r.Title == "" {
		return Result{}, ErrMissingTitle
	}
	if r.DurationMin == 0 {
		r.DurationMin, r.DefaultedDuration = DefaultDurationMin, true
	}
	return r, nil
}

func parseDuration(tok string) (int, bool) {
	m := durationRe.FindStringSubmatch(strings.ToLower(tok))
	if m == nil || (m[1] == "" && m[2] == "") {
		return 0, false
	}
	h, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	total := h*60 + min
	return total, total > 0
}

func parseDue(val string, today civil.Date) (civil.Date, error) {
	switch val {
	case "today", "tod":
		return today, nil
	case "tomorrow", "tmr", "tom":
		return today.AddDays(1), nil
	}
	if len(val) >= 3 {
		if wd, ok := weekdays[val[:3]]; ok && strings.HasPrefix(fullDay(wd), val) {
			// Next occurrence, today included: "!fri" on a Friday means today.
			return today.AddDays((int(wd) - int(today.Weekday()) + 7) % 7), nil
		}
	}
	d, err := civil.ParseDate(val)
	if err != nil {
		return civil.Date{}, fmt.Errorf("due date !%s: want !YYYY-MM-DD, !today, !tomorrow or !mon…!sun", val)
	}
	return d, nil
}

func fullDay(wd time.Weekday) string { return strings.ToLower(wd.String()) }

func parseRecurrence(val string) (string, error) {
	switch v := strings.ToLower(val); v {
	case "daily", "weekdays":
		return v, nil
	}
	bad := fmt.Errorf("recurrence *%s: want *daily, *weekdays or *weekly:MON,WED", val)
	days, ok := strings.CutPrefix(strings.ToLower(val), "weekly:")
	if !ok || days == "" {
		return "", bad
	}
	var seen [7]bool
	for _, d := range strings.Split(days, ",") {
		wd, ok := weekdays[d]
		if !ok {
			return "", bad
		}
		seen[wd] = true
	}
	// Canonical form: Monday-first order, upper case, no duplicates.
	var out []string
	for _, wd := range []time.Weekday{1, 2, 3, 4, 5, 6, 0} {
		if seen[wd] {
			out = append(out, strings.ToUpper(fullDay(wd)[:3]))
		}
	}
	return "weekly:" + strings.Join(out, ","), nil
}
