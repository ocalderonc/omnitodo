package store

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ocalderonc/omnitodo/internal/civil"
	"github.com/ocalderonc/omnitodo/internal/schedule"
)

var (
	ctx   = context.Background()
	today = civil.NewDate(2026, 11, 9)
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "sub", "omnitodo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func must[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func ptr[T any](v T) *T { return &v }

func titles(as []Activity) []string {
	var out []string
	for _, a := range as {
		out = append(out, a.Title)
	}
	return out
}

func eq[T comparable](t *testing.T, what string, got, want []T) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	for range 2 {
		s, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
}

func TestCatalog(t *testing.T) {
	s := open(t)
	fox := must[Company](t)(s.CreateCompany(ctx, Company{Name: "Fox Nation"}))
	if fox.Color != defaultColor {
		t.Errorf("default color = %q", fox.Color)
	}
	if _, err := s.CreateCompany(ctx, Company{Name: "fox nation"}); !errors.Is(err, ErrConflict) {
		t.Errorf("case-insensitive duplicate company: %v", err)
	}
	if _, err := s.CreateCompany(ctx, Company{Name: "X", Color: "red"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad color: %v", err)
	}
	b := must[Company](t)(s.CreateCompany(ctx, Company{Name: "Company B"}))

	must[Project](t)(s.CreateProject(ctx, Project{CompanyID: &fox.ID, Name: "LG"}))
	must[Project](t)(s.CreateProject(ctx, Project{CompanyID: &b.ID, Name: "Site"}))
	must[Project](t)(s.CreateProject(ctx, Project{Name: "Loose"}))
	if _, err := s.CreateProject(ctx, Project{CompanyID: &fox.ID, Name: "lg"}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate project in company: %v", err)
	}
	if _, err := s.CreateProject(ctx, Project{Name: "LOOSE"}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate company-less project: %v", err)
	}
	if _, err := s.CreateProject(ctx, Project{CompanyID: ptr(int64(999)), Name: "Ghost"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown company: %v", err)
	}

	p := must[Project](t)(s.ResolveProject(ctx, "lg"))
	if p.Name != "LG" || p.CompanyName != "Fox Nation" {
		t.Errorf("ResolveProject(lg) = %+v", p)
	}
	must[Project](t)(s.CreateProject(ctx, Project{CompanyID: &b.ID, Name: "LG"}))
	if _, err := s.ResolveProject(ctx, "LG"); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("same project name in two companies: %v", err)
	}
	if p := must[Project](t)(s.ResolveProject(ctx, "company b/lg")); p.CompanyName != "Company B" {
		t.Errorf("qualified resolve picked %q", p.CompanyName)
	}
	if _, err := s.ResolveProject(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing project: %v", err)
	}

	// Archived companies drop out of the default list; deleting one orphans its projects.
	fox.Archived = true
	if err := s.UpdateCompany(ctx, fox); err != nil {
		t.Fatal(err)
	}
	eq(t, "active companies", namesOf(must[[]Company](t)(s.ListCompanies(ctx, false))), []string{"Company B"})
	if err := s.DeleteCompany(ctx, fox.ID); err != nil {
		t.Fatal(err)
	}
	if p := must[Project](t)(s.ResolveProject(ctx, "Loose")); p.CompanyID != nil {
		t.Error("company-less project gained a company")
	}
	for _, p := range must[[]Project](t)(s.ListProjects(ctx, true)) {
		if p.Name == "LG" && p.CompanyName == "" && p.CompanyID != nil {
			t.Error("orphaned project still points at deleted company")
		}
	}
	if err := s.DeleteCompany(ctx, fox.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func namesOf(cs []Company) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func TestActivityLifecycle(t *testing.T) {
	s := open(t)
	fox := must[Company](t)(s.CreateCompany(ctx, Company{Name: "Fox Nation", Color: "#EA580C"}))
	lg := must[Project](t)(s.CreateProject(ctx, Project{CompanyID: &fox.ID, Name: "LG"}))

	a := must[Activity](t)(s.CreateActivity(ctx, ActivityInput{
		Title: "  Fix webhook  ", DurationMin: 90, ProjectID: &lg.ID, PlannedDate: &today,
		StartAt: ptr(civil.NewTime(14, 0)), DueDate: ptr(today.AddDays(3)), Tags: []string{"#Backend", "backend", "ops"},
	}))
	if a.Title != "Fix webhook" || a.Kind != schedule.KindTask || a.Status != schedule.StatusTodo {
		t.Errorf("defaults: %+v", a)
	}
	if a.ProjectName != "LG" || a.CompanyName != "Fox Nation" || a.Color != "#ea580c" {
		t.Errorf("joined fields: %q %q %q", a.ProjectName, a.CompanyName, a.Color)
	}
	if *a.StartAt != civil.NewTime(14, 0) || *a.DueDate != today.AddDays(3) || *a.PlannedDate != today {
		t.Errorf("dates round-trip: %v %v %v", a.StartAt, a.DueDate, a.PlannedDate)
	}
	var tags []string
	for _, tg := range a.Tags {
		tags = append(tags, tg.Name)
	}
	eq(t, "tags", tags, []string{"Backend", "ops"})
	if a.CreatedAt.IsZero() {
		t.Error("created_at not parsed")
	}

	upd := must[Activity](t)(s.UpdateActivity(ctx, a.ID, ActivityInput{
		Kind: schedule.KindDistractor, Title: "Webhook call", DurationMin: 30, Description: "with LG", Tags: []string{"ops"},
	}))
	if upd.Kind != schedule.KindDistractor || upd.ProjectID != nil || upd.StartAt != nil || upd.Description != "with LG" || len(upd.Tags) != 1 {
		t.Errorf("update: %+v", upd)
	}
	if *upd.PlannedDate != today {
		t.Error("update must not change the day")
	}

	if st := must[schedule.Status](t)(s.ToggleDone(ctx, a.ID)); st != schedule.StatusDone {
		t.Errorf("toggle 1 = %s", st)
	}
	if st := must[schedule.Status](t)(s.ToggleDone(ctx, a.ID)); st != schedule.StatusTodo {
		t.Errorf("toggle 2 = %s", st)
	}
	if err := s.SetStatus(ctx, a.ID, "nope"); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad status: %v", err)
	}

	if err := s.DeleteActivity(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetActivity(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
	if _, err := s.ToggleDone(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("toggle deleted: %v", err)
	}
}

func TestActivityValidation(t *testing.T) {
	s := open(t)
	for name, in := range map[string]ActivityInput{
		"empty title":         {Title: " ", DurationMin: 10},
		"zero duration":       {Title: "x"},
		"bad kind":            {Title: "x", DurationMin: 10, Kind: "meeting"},
		"bad recurrence":      {Title: "x", DurationMin: 10, Recurrence: "monthly", PlannedDate: &today},
		"template no start":   {Title: "x", DurationMin: 10, Recurrence: "daily"},
		"start past midnight": {Title: "x", DurationMin: 10, StartAt: ptr(civil.Time(24 * 60))},
		"unknown project":     {Title: "x", DurationMin: 10, ProjectID: ptr(int64(42))},
	} {
		if _, err := s.CreateActivity(ctx, in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestDayOrderingAndMoves(t *testing.T) {
	s := open(t)
	add := func(title string, d *civil.Date, kind schedule.Kind) Activity {
		return must[Activity](t)(s.CreateActivity(ctx, ActivityInput{Title: title, DurationMin: 10, PlannedDate: d, Kind: kind}))
	}
	a := add("A", &today, "")
	add("B", &today, "")
	c := add("C", &today, "")
	add("Meeting", &today, schedule.KindDistractor)
	backlog := add("Someday", nil, "")
	eq(t, "creation order", titles(must[[]Activity](t)(s.ListDay(ctx, today))), []string{"A", "B", "C", "Meeting"})

	if err := s.Reorder(ctx, &today, []int64{c.ID, a.ID}); err != nil {
		t.Fatal(err)
	}
	day := must[[]Activity](t)(s.ListDay(ctx, today))
	eq(t, "after partial reorder", titles(day), []string{"C", "A", "B", "Meeting"})
	if day[0].Position != 1000 || day[3].Position != 4000 {
		t.Errorf("positions not renumbered: %d..%d", day[0].Position, day[3].Position)
	}
	if err := s.Reorder(ctx, &today, []int64{a.ID, backlog.ID}); !errors.Is(err, ErrInvalid) {
		t.Errorf("reorder with foreign id: %v", err)
	}
	if err := s.Reorder(ctx, &today, []int64{a.ID, a.ID}); !errors.Is(err, ErrInvalid) {
		t.Errorf("reorder with duplicate id: %v", err)
	}

	// Plan the backlog item for today: it goes last.
	if err := s.MoveToDate(ctx, backlog.ID, &today); err != nil {
		t.Fatal(err)
	}
	eq(t, "after planning", titles(must[[]Activity](t)(s.ListDay(ctx, today))), []string{"C", "A", "B", "Meeting", "Someday"})
	if got := must[[]Activity](t)(s.ListBacklog(ctx, BacklogFilter{})); len(got) != 0 {
		t.Errorf("backlog not empty: %v", titles(got))
	}

	// Rollover: unfinished tasks move in order behind what tomorrow already has;
	// done tasks and distractors stay.
	tomorrow := today.AddDays(1)
	add("Tomorrow's own", &tomorrow, "")
	must[schedule.Status](t)(s.ToggleDone(ctx, a.ID))
	moved := must[int](t)(s.Rollover(ctx, today, tomorrow))
	if moved != 3 {
		t.Errorf("moved = %d, want 3", moved)
	}
	eq(t, "today after rollover", titles(must[[]Activity](t)(s.ListDay(ctx, today))), []string{"A", "Meeting"})
	eq(t, "tomorrow after rollover", titles(must[[]Activity](t)(s.ListDay(ctx, tomorrow))), []string{"Tomorrow's own", "C", "B", "Someday"})
}

func TestBacklogFilters(t *testing.T) {
	s := open(t)
	fox := must[Company](t)(s.CreateCompany(ctx, Company{Name: "Fox"}))
	lg := must[Project](t)(s.CreateProject(ctx, Project{CompanyID: &fox.ID, Name: "LG"}))
	other := must[Project](t)(s.CreateProject(ctx, Project{Name: "Other"}))
	must[Activity](t)(s.CreateActivity(ctx, ActivityInput{Title: "lg backend", DurationMin: 5, ProjectID: &lg.ID, Tags: []string{"backend"}}))
	must[Activity](t)(s.CreateActivity(ctx, ActivityInput{Title: "lg plain", DurationMin: 5, ProjectID: &lg.ID}))
	must[Activity](t)(s.CreateActivity(ctx, ActivityInput{Title: "other backend", DurationMin: 5, ProjectID: &other.ID, Tags: []string{"backend"}}))
	done := must[Activity](t)(s.CreateActivity(ctx, ActivityInput{Title: "done", DurationMin: 5}))
	must[schedule.Status](t)(s.ToggleDone(ctx, done.ID))
	must[Activity](t)(s.CreateActivity(ctx, ActivityInput{Title: "planned", DurationMin: 5, PlannedDate: &today}))

	backend := must[[]Tag](t)(s.ListTags(ctx))[0]
	eq(t, "all", titles(must[[]Activity](t)(s.ListBacklog(ctx, BacklogFilter{}))), []string{"lg backend", "lg plain", "other backend"})
	eq(t, "company", titles(must[[]Activity](t)(s.ListBacklog(ctx, BacklogFilter{CompanyID: &fox.ID}))), []string{"lg backend", "lg plain"})
	eq(t, "project", titles(must[[]Activity](t)(s.ListBacklog(ctx, BacklogFilter{ProjectID: &other.ID}))), []string{"other backend"})
	eq(t, "tag", titles(must[[]Activity](t)(s.ListBacklog(ctx, BacklogFilter{TagID: &backend.ID}))), []string{"lg backend", "other backend"})
	eq(t, "company+tag", titles(must[[]Activity](t)(s.ListBacklog(ctx, BacklogFilter{CompanyID: &fox.ID, TagID: &backend.ID}))), []string{"lg backend"})
}

func TestTemplates(t *testing.T) {
	s := open(t)
	tpl := must[Activity](t)(s.CreateActivity(ctx, ActivityInput{
		Kind: schedule.KindDistractor, Title: "Pickup", DurationMin: 40, StartAt: ptr(civil.NewTime(12, 10)),
		PlannedDate: &today, Recurrence: "WEEKLY:fri,mon",
	}))
	if !tpl.IsTemplate || tpl.PlannedDate != nil || *tpl.RecurFrom != today || tpl.Recurrence != "weekly:MON,FRI" {
		t.Errorf("template: %+v", tpl)
	}
	if got := must[[]Activity](t)(s.ListDay(ctx, today)); len(got) != 0 {
		t.Error("a template showed up on a day")
	}
	eq(t, "templates", titles(must[[]Activity](t)(s.ListTemplates(ctx))), []string{"Pickup"})
	if _, err := s.UpdateActivity(ctx, tpl.ID, ActivityInput{Title: "Pickup", DurationMin: 40}); !errors.Is(err, ErrInvalid) {
		t.Errorf("clearing recurrence: %v", err)
	}
	if err := s.MoveToDate(ctx, tpl.ID, &today); !errors.Is(err, ErrInvalid) {
		t.Errorf("moving a template: %v", err)
	}
	if _, err := s.ToggleDone(ctx, tpl.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("toggling a template: %v", err)
	}
}

func TestSettings(t *testing.T) {
	s := open(t)
	got := must[Settings](t)(s.GetSettings(ctx))
	if got.DayStart != civil.NewTime(8, 0) || got.DayEnd != civil.NewTime(18, 0) || got.Timezone != "America/El_Salvador" {
		t.Errorf("defaults: %+v", got)
	}
	want := Settings{DayStart: civil.NewTime(7, 30), DayEnd: civil.NewTime(17, 0), Timezone: "America/Mexico_City"}
	if err := s.UpdateSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got := must[Settings](t)(s.GetSettings(ctx)); got != want {
		t.Errorf("round trip: %+v", got)
	}
	for _, bad := range []Settings{
		{DayStart: 600, DayEnd: 600, Timezone: "UTC"},
		{DayStart: 600, DayEnd: 24 * 60, Timezone: "UTC"},
		{DayStart: 600, DayEnd: 700, Timezone: "Mars/Olympus"},
	} {
		if err := s.UpdateSettings(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("UpdateSettings(%+v) = %v", bad, err)
		}
	}
}

func TestSeedBuildsAPlausibleDay(t *testing.T) {
	s := open(t)
	if err := s.Seed(ctx, today); err != nil {
		t.Fatal(err)
	}
	if err := s.Seed(ctx, today); !errors.Is(err, ErrNotEmpty) {
		t.Errorf("second seed: %v", err)
	}
	day := must[[]Activity](t)(s.ListDay(ctx, today))
	items := make([]schedule.Activity, len(day))
	for i, a := range day {
		items[i] = a.ScheduleItem()
	}
	settings := must[Settings](t)(s.GetSettings(ctx))
	blocks, sum := schedule.BuildDay(today, items, settings.Schedule())
	if len(blocks) == 0 || sum.TaskMinutes != 275 || sum.ByCompany["Fox Nation"].Task != 135 {
		t.Errorf("seeded day summary: %+v", sum)
	}
	overdue := 0
	for _, b := range blocks {
		if b.Has(schedule.FlagOverdue) {
			overdue++
		}
	}
	if overdue != 1 {
		t.Errorf("overdue blocks = %d, want 1 (the invoice)", overdue)
	}
}

func TestSchemaRejectsMalformedValues(t *testing.T) {
	s := open(t)
	for _, tc := range []struct{ col, val string }{
		{"planned_date", "2026-02-30"},
		{"planned_date", "2026-11-9"},
		{"due_date", "tomorrow"},
		{"start_at", "24:00"},
		{"start_at", "9:00"},
		{"start_at", "12:00:00"},
	} {
		_, err := s.db.ExecContext(ctx, `INSERT INTO activity (title, duration_min, `+tc.col+`) VALUES ('x', 5, ?)`, tc.val)
		if err == nil {
			t.Errorf("%s = %q was accepted", tc.col, tc.val)
		}
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO activity (title, duration_min, planned_date, start_at) VALUES ('x', 5, '2026-11-09', '07:05')`); err != nil {
		t.Errorf("valid values rejected: %v", err)
	}
}

func TestBackup(t *testing.T) {
	s := open(t)
	if err := s.Seed(ctx, today); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "backup.db")
	for range 2 { // the second run replaces the first
		if err := s.Backup(ctx, path); err != nil {
			t.Fatal(err)
		}
	}
	b, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if got := must[[]Activity](t)(b.ListDay(ctx, today)); len(got) != 6 {
		t.Errorf("backup has %d activities today, want 6", len(got))
	}
}
