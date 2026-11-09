// Package schedule lays out one day's activities on a timeline.
//
// It is pure: no database, no clock. The caller passes the day being viewed,
// the activities planned for it and the working-day settings.
package schedule

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/ocalderonc/omnitodo/internal/civil"
)

type Kind string

const (
	KindTask       Kind = "task"
	KindDistractor Kind = "distractor"
	KindFree       Kind = "free"
)

type Status string

const (
	StatusTodo    Status = "todo"
	StatusDone    Status = "done"
	StatusSkipped Status = "skipped"
)

type Flag string

const (
	FlagConflict Flag = "conflict" // two anchored items overlap
	FlagOverflow Flag = "overflow" // ends after day_end
	FlagOverdue  Flag = "overdue"  // task still todo with a due date before the day
)

// Activity is the subset of an activity the timeline needs.
type Activity struct {
	ID          int64
	Kind        Kind // task or distractor
	Status      Status
	DurationMin int
	Position    int
	StartAt     *civil.Time // nil = floating
	DueDate     *civil.Date
	Company     string // display name, "" if none
	Project     string // display name, "" if none
}

type Settings struct {
	DayStart, DayEnd civil.Time
}

type Block struct {
	ActivityID int64 // 0 for free blocks
	Kind       Kind
	Start, End civil.Time
	Anchored   bool
	Flags      []Flag
}

func (b Block) Has(f Flag) bool { return slices.Contains(b.Flags, f) }

type Summary struct {
	TaskMinutes       int
	DistractorMinutes int
	TotalMinutes      int
	AvailableMinutes  int
	FreeMinutes       int // Available - Total; negative means overbooked
	ByCompany         map[string]Split
	ByProject         map[string]Split
	DoneMinutes       int // tasks marked done
	RemainingMinutes  int // tasks still todo
}

func (s Summary) Overbooked() bool { return s.FreeMinutes < 0 }

// Split is the minutes a company or project takes in a day, separated so the
// UI can show work alone ("Fox Nation 4h 30m of work") or everything together.
type Split struct {
	Task       int
	Distractor int
}

func (s Split) Total() int { return s.Task + s.Distractor }

// BuildDay places anchored items at their fixed time and flows floating items,
// in position order, from day_start into the gaps between anchors. A floating
// item that does not fit before the next anchor moves after it (no splitting).
// Gaps inside [day_start, day_end] come back as free blocks. The result is
// sorted by start time.
func BuildDay(day civil.Date, items []Activity, s Settings) ([]Block, Summary) {
	sum := Summary{
		AvailableMinutes: max(0, int(s.DayEnd-s.DayStart)),
		ByCompany:        map[string]Split{},
		ByProject:        map[string]Split{},
	}

	var active []Activity
	for _, a := range items {
		if a.Status != StatusSkipped && a.DurationMin > 0 {
			active = append(active, a)
		}
	}
	slices.SortStableFunc(active, func(a, b Activity) int {
		return cmp.Or(cmp.Compare(a.Position, b.Position), cmp.Compare(a.ID, b.ID))
	})

	var anchored, floating []Block
	for _, a := range active {
		b := Block{ActivityID: a.ID, Kind: a.Kind}
		if a.Kind == KindTask && a.Status == StatusTodo && a.DueDate != nil && a.DueDate.Before(day) {
			b.Flags = append(b.Flags, FlagOverdue)
		}
		if a.StartAt != nil {
			b.Anchored, b.Start, b.End = true, *a.StartAt, a.StartAt.Add(a.DurationMin)
			anchored = append(anchored, b)
		} else {
			b.End = civil.Time(a.DurationMin) // provisional: holds the duration until placed
			floating = append(floating, b)
		}
		accumulate(&sum, a)
	}
	sum.TotalMinutes = sum.TaskMinutes + sum.DistractorMinutes
	sum.FreeMinutes = sum.AvailableMinutes - sum.TotalMinutes

	slices.SortStableFunc(anchored, func(a, b Block) int { return cmp.Compare(a.Start, b.Start) })
	for i := range anchored {
		for j := i + 1; j < len(anchored) && anchored[j].Start < anchored[i].End; j++ {
			addFlag(&anchored[i], FlagConflict)
			addFlag(&anchored[j], FlagConflict)
		}
	}

	cursor := s.DayStart
	for i := range floating {
		dur := floating[i].End
		t := cursor
		// One pass in start order is enough: pushing t past an anchor can only
		// create an overlap with anchors that start later.
		for _, a := range anchored {
			if t < a.End && t+dur > a.Start {
				t = a.End
			}
		}
		floating[i].Start, floating[i].End = t, t+dur
		cursor = t + dur
	}

	blocks := append(anchored, floating...)
	for i := range blocks {
		if blocks[i].End > s.DayEnd {
			addFlag(&blocks[i], FlagOverflow)
		}
	}
	slices.SortStableFunc(blocks, func(a, b Block) int {
		return cmp.Or(cmp.Compare(a.Start, b.Start), cmp.Compare(a.End, b.End))
	})
	return withFree(blocks, s), sum
}

func accumulate(sum *Summary, a Activity) {
	switch a.Kind {
	case KindTask:
		sum.TaskMinutes += a.DurationMin
		if a.Status == StatusDone {
			sum.DoneMinutes += a.DurationMin
		} else {
			sum.RemainingMinutes += a.DurationMin
		}
	case KindDistractor:
		sum.DistractorMinutes += a.DurationMin
	}
	addSplit(sum.ByCompany, a.Company, a)
	addSplit(sum.ByProject, a.Project, a)
}

func addSplit(m map[string]Split, key string, a Activity) {
	if key == "" {
		return
	}
	split := m[key]
	if a.Kind == KindDistractor {
		split.Distractor += a.DurationMin
	} else {
		split.Task += a.DurationMin
	}
	m[key] = split
}

func addFlag(b *Block, f Flag) {
	if !b.Has(f) {
		b.Flags = append(b.Flags, f)
	}
}

// withFree interleaves free blocks for the uncovered parts of [DayStart, DayEnd].
func withFree(blocks []Block, s Settings) []Block {
	out := make([]Block, 0, 2*len(blocks)+1)
	cursor := s.DayStart // everything before cursor is covered (or outside the day)
	free := func(until civil.Time) {
		until = min(until, s.DayEnd)
		if until > cursor {
			out = append(out, Block{Kind: KindFree, Start: cursor, End: until})
			cursor = until
		}
	}
	for _, b := range blocks {
		free(b.Start)
		out = append(out, b)
		cursor = max(cursor, b.End)
	}
	free(s.DayEnd)
	return out
}

// FormatDuration renders minutes as "5h 40m", "45m", "2h" or "-1h 10m".
func FormatDuration(min int) string {
	sign := ""
	if min < 0 {
		sign, min = "-", -min
	}
	h, m := min/60, min%60
	switch {
	case h == 0:
		return fmt.Sprintf("%s%dm", sign, m)
	case m == 0:
		return fmt.Sprintf("%s%dh", sign, h)
	default:
		return fmt.Sprintf("%s%dh %dm", sign, h, m)
	}
}
