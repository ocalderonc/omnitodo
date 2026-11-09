package schedule

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ocalderonc/omnitodo/internal/civil"
)

var (
	monday  = civil.NewDate(2026, 11, 9)
	workday = Settings{DayStart: civil.NewTime(8, 0), DayEnd: civil.NewTime(18, 0)}
	morning = Settings{DayStart: civil.NewTime(8, 0), DayEnd: civil.NewTime(10, 0)}
)

func at(s string) *civil.Time {
	t, err := civil.ParseTime(s)
	if err != nil {
		panic(err)
	}
	return &t
}

func on(s string) *civil.Date {
	d, err := civil.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return &d
}

func task(id int64, pos, dur int) Activity {
	return Activity{ID: id, Kind: KindTask, Status: StatusTodo, Position: pos, DurationMin: dur}
}

func distractor(id int64, start string, dur int) Activity {
	return Activity{ID: id, Kind: KindDistractor, Status: StatusTodo, DurationMin: dur, StartAt: at(start)}
}

func with(a Activity, f func(*Activity)) Activity { f(&a); return a }

// render turns blocks into "id 08:00-09:00 [flags]" lines; free blocks use "_".
func render(blocks []Block) string {
	var lines []string
	for _, b := range blocks {
		id := "_"
		if b.Kind != KindFree {
			id = fmt.Sprint(b.ActivityID)
		}
		line := fmt.Sprintf("%s %s-%s", id, b.Start, b.End)
		if b.Anchored {
			line += " @"
		}
		for _, f := range b.Flags {
			line += " " + string(f)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func TestBuildDayTimeline(t *testing.T) {
	tests := []struct {
		name     string
		settings Settings
		items    []Activity
		want     []string
	}{
		{
			name:     "empty day is all free",
			settings: workday,
			want:     []string{"_ 08:00-18:00"},
		},
		{
			name:     "floating tasks flow in position order, not input order",
			settings: morning,
			items:    []Activity{task(2, 2000, 30), task(1, 1000, 45)},
			want:     []string{"1 08:00-08:45", "2 08:45-09:15", "_ 09:15-10:00"},
		},
		{
			name:     "equal positions fall back to id",
			settings: morning,
			items:    []Activity{task(9, 1000, 30), task(3, 1000, 30)},
			want:     []string{"3 08:00-08:30", "9 08:30-09:00", "_ 09:00-10:00"},
		},
		{
			name:     "floating task that does not fit before an anchor moves after it",
			settings: workday,
			items: []Activity{
				task(1, 1000, 60),
				task(2, 2000, 60), // would be 09:00-10:00, overlaps 09:30 meeting
				task(3, 3000, 15),
				distractor(10, "09:30", 30),
			},
			want: []string{
				"1 08:00-09:00",
				"_ 09:00-09:30",
				"10 09:30-10:00 @",
				"2 10:00-11:00",
				"3 11:00-11:15",
				"_ 11:15-18:00",
			},
		},
		{
			name:     "no backfilling: a later short task does not jump into an earlier gap",
			settings: workday,
			items:    []Activity{task(1, 1000, 90), task(2, 2000, 10), distractor(10, "09:00", 60)},
			want: []string{
				"_ 08:00-09:00",
				"10 09:00-10:00 @",
				"1 10:00-11:30",
				"2 11:30-11:40",
				"_ 11:40-18:00",
			},
		},
		{
			name:     "task that exactly fills the gap before an anchor stays there",
			settings: workday,
			items:    []Activity{task(1, 1000, 60), distractor(10, "09:00", 30)},
			want:     []string{"1 08:00-09:00", "10 09:00-09:30 @", "_ 09:30-18:00"},
		},
		{
			name:     "floating task skips back-to-back anchors",
			settings: workday,
			items: []Activity{
				task(1, 1000, 30),
				distractor(10, "08:15", 30),
				distractor(11, "08:45", 30),
			},
			want: []string{"_ 08:00-08:15", "10 08:15-08:45 @", "11 08:45-09:15 @", "1 09:15-09:45", "_ 09:45-18:00"},
		},
		{
			name:     "overlapping anchors are both flagged as conflicts",
			settings: workday,
			items: []Activity{
				distractor(10, "12:10", 40), // pickup
				distractor(11, "12:30", 30), // meeting
				distractor(12, "13:00", 30), // touches 11 but does not overlap
			},
			want: []string{
				"_ 08:00-12:10",
				"10 12:10-12:50 @ conflict",
				"11 12:30-13:00 @ conflict",
				"12 13:00-13:30 @",
				"_ 13:30-18:00",
			},
		},
		{
			name:     "floating task flows around a nested conflict",
			settings: workday,
			items: []Activity{
				task(1, 1000, 30),
				distractor(10, "08:10", 60),
				distractor(11, "08:20", 10),
			},
			want: []string{
				"_ 08:00-08:10",
				"10 08:10-09:10 @ conflict",
				"11 08:20-08:30 @ conflict",
				"1 09:10-09:40",
				"_ 09:40-18:00",
			},
		},
		{
			name:     "blocks ending after day_end overflow",
			settings: morning,
			items:    []Activity{task(1, 1000, 90), task(2, 2000, 60), distractor(10, "09:45", 15)},
			want: []string{
				"1 08:00-09:30",
				"_ 09:30-09:45",
				"10 09:45-10:00 @",
				"2 10:00-11:00 overflow",
			},
		},
		{
			name:     "anchor before day_start is kept, floating still starts at day_start",
			settings: workday,
			items:    []Activity{distractor(10, "07:30", 45), task(1, 1000, 30)},
			want:     []string{"10 07:30-08:15 @", "1 08:15-08:45", "_ 08:45-18:00"},
		},
		{
			name:     "skipped and zero-length items are ignored; done items keep their slot",
			settings: morning,
			items: []Activity{
				with(task(1, 1000, 30), func(a *Activity) { a.Status = StatusSkipped }),
				with(task(2, 2000, 30), func(a *Activity) { a.Status = StatusDone }),
				task(3, 3000, 0),
				task(4, 4000, 30),
			},
			want: []string{"2 08:00-08:30", "4 08:30-09:00", "_ 09:00-10:00"},
		},
		{
			name:     "overdue applies to todo tasks due before the day, only",
			settings: morning,
			items: []Activity{
				with(task(1, 1000, 10), func(a *Activity) { a.DueDate = on("2026-11-08") }),
				with(task(2, 2000, 10), func(a *Activity) { a.DueDate = on("2026-11-09") }), // due today: fine
				with(task(3, 3000, 10), func(a *Activity) { a.DueDate = on("2026-11-01"); a.Status = StatusDone }),
				with(distractor(10, "09:00", 10), func(a *Activity) { a.DueDate = on("2026-11-01") }),
			},
			want: []string{
				"1 08:00-08:10 overdue",
				"2 08:10-08:20",
				"3 08:20-08:30",
				"_ 08:30-09:00",
				"10 09:00-09:10 @",
				"_ 09:10-10:00",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks, _ := BuildDay(monday, tt.items, tt.settings)
			if got, want := render(blocks), strings.Join(tt.want, "\n"); got != want {
				t.Errorf("timeline mismatch\n got:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func TestBuildDaySummary(t *testing.T) {
	items := []Activity{
		with(task(1, 1000, 120), func(a *Activity) { a.Company, a.Project = "Fox Nation", "LG" }),
		with(task(2, 2000, 150), func(a *Activity) { a.Company, a.Project = "Fox Nation", "Core"; a.Status = StatusDone }),
		with(task(3, 3000, 120), func(a *Activity) { a.Company, a.Project = "Company B", "Site" }),
		with(task(4, 4000, 60), func(a *Activity) { a.Status = StatusSkipped; a.Company = "Company B" }),
		with(distractor(10, "12:10", 40), func(a *Activity) {}),
		with(distractor(11, "15:00", 30), func(a *Activity) { a.Company = "Fox Nation" }), // standup
		task(5, 5000, 120), // no company
	}
	_, got := BuildDay(monday, items, workday)
	want := Summary{
		TaskMinutes:       510,
		DistractorMinutes: 70,
		TotalMinutes:      580,
		AvailableMinutes:  600,
		FreeMinutes:       20,
		ByCompany: map[string]Split{
			"Fox Nation": {Task: 270, Distractor: 30},
			"Company B":  {Task: 120},
		},
		ByProject:        map[string]Split{"LG": {Task: 120}, "Core": {Task: 150}, "Site": {Task: 120}},
		DoneMinutes:      150,
		RemainingMinutes: 360,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("summary\n got: %+v\nwant: %+v", got, want)
	}
	if fox := got.ByCompany["Fox Nation"]; fox.Total() != 300 {
		t.Errorf("Fox Nation total = %d, want 300", fox.Total())
	}
	if got.Overbooked() {
		t.Error("20 free minutes reported as overbooked")
	}

	_, over := BuildDay(monday, append(items, task(6, 6000, 30)), workday)
	if over.FreeMinutes != -10 || !over.Overbooked() {
		t.Errorf("FreeMinutes = %d, Overbooked = %v; want -10, true", over.FreeMinutes, over.Overbooked())
	}
}

func TestBuildDayDoesNotMutateInput(t *testing.T) {
	items := []Activity{task(2, 2000, 30), task(1, 1000, 30)}
	BuildDay(monday, items, workday)
	if items[0].ID != 2 {
		t.Error("BuildDay reordered the caller's slice")
	}
}

func TestFormatDuration(t *testing.T) {
	for in, want := range map[int]string{0: "0m", 45: "45m", 60: "1h", 120: "2h", 340: "5h 40m", -10: "-10m", -70: "-1h 10m"} {
		if got := FormatDuration(in); got != want {
			t.Errorf("FormatDuration(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestSummaryCompanyAndProjectWithSameName(t *testing.T) {
	a := with(task(1, 1000, 60), func(a *Activity) { a.Company, a.Project = "LG", "LG" })
	_, got := BuildDay(monday, []Activity{a}, workday)
	if got.ByCompany["LG"].Task != 60 || got.ByProject["LG"].Task != 60 {
		t.Errorf("ByCompany = %v, ByProject = %v; want 60 task minutes in each", got.ByCompany, got.ByProject)
	}
}
