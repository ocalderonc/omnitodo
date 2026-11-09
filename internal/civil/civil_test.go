package civil

import (
	"testing"
	"time"
)

func TestParseTime(t *testing.T) {
	ok := map[string]Time{"00:00": 0, "8:05": 485, "08:05": 485, "14:00": 840, "23:59": 1439}
	for in, want := range ok {
		got, err := ParseTime(in)
		if err != nil || got != want {
			t.Errorf("ParseTime(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "24:00", "12:60", "12", "12:5", "123:00", "1a:00", "+1:00", "12:00x", ":30"} {
		if _, err := ParseTime(in); err == nil {
			t.Errorf("ParseTime(%q) succeeded, want error", in)
		}
	}
}

func TestTimeString(t *testing.T) {
	for in, want := range map[Time]string{0: "00:00", 485: "08:05", 1500: "25:00", -30: "-00:30"} {
		if got := in.String(); got != want {
			t.Errorf("Time(%d).String() = %q, want %q", int(in), got, want)
		}
	}
}

func TestDate(t *testing.T) {
	d, err := ParseDate("2026-11-09")
	if err != nil {
		t.Fatal(err)
	}
	if d.Weekday() != time.Monday {
		t.Errorf("weekday = %v, want Monday", d.Weekday())
	}
	if got := d.AddDays(53).String(); got != "2027-01-01" {
		t.Errorf("AddDays across year = %s", got)
	}
	if !d.Before(d.AddDays(1)) || d.Before(d) || !d.AddDays(1).After(d) {
		t.Error("ordering is wrong")
	}
	if got := NewDate(2026, 11, 31); got.String() != "2026-12-01" {
		t.Errorf("NewDate does not normalise: %s", got)
	}
	for _, in := range []string{"2026-13-01", "2026-02-30", "09/11/2026", ""} {
		if _, err := ParseDate(in); err == nil {
			t.Errorf("ParseDate(%q) succeeded, want error", in)
		}
	}
}
