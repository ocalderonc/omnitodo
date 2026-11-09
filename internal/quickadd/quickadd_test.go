package quickadd

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ocalderonc/omnitodo/internal/civil"
)

var today = civil.NewDate(2026, 11, 9) // a Monday

func tm(s string) *civil.Time {
	t, err := civil.ParseTime(s)
	if err != nil {
		panic(err)
	}
	return &t
}

func dt(s string) *civil.Date {
	d, err := civil.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return &d
}

func TestParse(t *testing.T) {
	tests := []struct {
		in   string
		want Result
	}{
		{"Review PR 45m +LG", Result{Title: "Review PR", DurationMin: 45, Project: "LG"}},
		{
			"Fix LG webhook retry 1h30m +LG #backend @14:00 !2026-11-12",
			Result{Title: "Fix LG webhook retry", DurationMin: 90, Project: "LG", Tags: []string{"backend"}, StartAt: tm("14:00"), DueDate: dt("2026-11-12")},
		},
		{
			"Kindergarten pickup 40m ~d @12:10 *weekdays",
			Result{Title: "Kindergarten pickup", DurationMin: 40, Distractor: true, StartAt: tm("12:10"), Recurrence: "weekdays"},
		},
		{"tokens anywhere 2h #a write docs", Result{Title: "tokens anywhere write docs", DurationMin: 120, Tags: []string{"a"}}},
		{"Upper case 1H30M", Result{Title: "Upper case", DurationMin: 90}},
		{"Minutes only 90m", Result{Title: "Minutes only", DurationMin: 90}},
		{"Last duration wins 2h 30m", Result{Title: "Last duration wins 2h", DurationMin: 30}},
		{"Plan Q2 roadmap 1h", Result{Title: "Plan Q2 roadmap", DurationMin: 60}},
		{"Zero is not a duration 0m 15m", Result{Title: "Zero is not a duration 0m", DurationMin: 15}},
		{"Tags dedupe #Ops #ops #db 10m", Result{Title: "Tags dedupe", DurationMin: 10, Tags: []string{"Ops", "db"}}},
		{"Lone sigils stay + @ ! 10m", Result{Title: "Lone sigils stay + @ !", DurationMin: 10}},
		{"Email me@x.com and C# 10m", Result{Title: "Email me@x.com and C#", DurationMin: 10}},
		{"Early anchor 15m @7:05", Result{Title: "Early anchor", DurationMin: 15, StartAt: tm("07:05")}},
		{"Due today 5m !today", Result{Title: "Due today", DurationMin: 5, DueDate: dt("2026-11-09")}},
		{"Due tomorrow 5m !Tomorrow", Result{Title: "Due tomorrow", DurationMin: 5, DueDate: dt("2026-11-10")}},
		{"Due fri 5m !fri", Result{Title: "Due fri", DurationMin: 5, DueDate: dt("2026-11-13")}},
		{"Due friday 5m !friday", Result{Title: "Due friday", DurationMin: 5, DueDate: dt("2026-11-13")}},
		{"Due mon is today 5m !mon", Result{Title: "Due mon is today", DurationMin: 5, DueDate: dt("2026-11-09")}},
		{"Due sun 5m !sun", Result{Title: "Due sun", DurationMin: 5, DueDate: dt("2026-11-15")}},
		{"Standup 15m ~D *weekly:wed,MON,wed", Result{Title: "Standup", DurationMin: 15, Distractor: true, Recurrence: "weekly:MON,WED"}},
		{"Weekend 15m *weekly:sun,sat", Result{Title: "Weekend", DurationMin: 15, Recurrence: "weekly:SAT,SUN"}},
		{"No duration gets the default", Result{Title: "No duration gets the default", DurationMin: DefaultDurationMin, DefaultedDuration: true}},
		{"Gym 1h *Daily", Result{Title: "Gym", DurationMin: 60, Recurrence: "daily"}},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := Parse(tt.in, today)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("\n got: %+v\nwant: %+v", got, tt.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		in, wantErr string
	}{
		{"45m +LG #x", ErrMissingTitle.Error()},
		{"", ErrMissingTitle.Error()},
		{"Bad time 10m @25:00", "start time @25:00"},
		{"Bad time 10m @noon", "start time @noon"},
		{"Bad due 10m !someday", "due date !someday"},
		{"Bad due 10m !2026-02-30", "due date"},
		{"Bad kind 10m ~x", "unknown kind ~x"},
		{"Bad rec 10m *monthly", "recurrence *monthly"},
		{"Bad rec 10m *weekly:", "recurrence"},
		{"Bad rec 10m *weekly:MON,FUN", "recurrence"},
		{"Two projects 10m +A +B", "two projects"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			_, err := Parse(tt.in, today)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
	if _, err := Parse("45m", today); !errors.Is(err, ErrMissingTitle) {
		t.Errorf("missing title is not ErrMissingTitle: %v", err)
	}
}
