package recurrence

import (
	"testing"

	"github.com/ocalderonc/omnitodo/internal/civil"
)

func TestParseAndMatch(t *testing.T) {
	monday := civil.NewDate(2026, 11, 9)
	tests := []struct {
		in, canonical string
		matches       string // one char per day Mon..Sun
	}{
		{"daily", "daily", "XXXXXXX"},
		{"Weekdays", "weekdays", "XXXXX.."},
		{"weekly:wed,MON,wed", "weekly:MON,WED", "X.X...."},
		{"WEEKLY:sun,sat", "weekly:SAT,SUN", ".....XX"},
	}
	for _, tt := range tests {
		r, err := Parse(tt.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.in, err)
		}
		if r.String() != tt.canonical {
			t.Errorf("Parse(%q).String() = %q, want %q", tt.in, r.String(), tt.canonical)
		}
		for i, c := range tt.matches {
			if got := r.Matches(monday.AddDays(i)); got != (c == 'X') {
				t.Errorf("%s matches %s = %v", tt.in, monday.AddDays(i).Weekday(), got)
			}
		}
	}
	for _, in := range []string{"", "monthly", "weekly:", "weekly:MON,FUN", "weekly:MON,", "weekly"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", in)
		}
	}
}
