package store

import (
	"context"

	"github.com/ocalderonc/omnitodo/internal/civil"
	"github.com/ocalderonc/omnitodo/internal/schedule"
)

// Seed fills an empty database with sample companies, projects and a day of
// activities around today, so the UI has something to show. It refuses to
// touch a database that already has companies or activities.
func (s *Store) Seed(ctx context.Context, today civil.Date) error {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM company) + (SELECT count(*) FROM activity)`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrNotEmpty
	}

	companies := map[string]*Company{
		"Fox Nation": {Name: "Fox Nation", Color: "#ea580c"},
		"Company B":  {Name: "Company B", Color: "#2563eb"},
		"Personal":   {Name: "Personal", Color: "#16a34a"},
	}
	for _, name := range []string{"Fox Nation", "Company B", "Personal"} {
		c, err := s.CreateCompany(ctx, *companies[name])
		if err != nil {
			return err
		}
		*companies[name] = c
	}
	projects := map[string]int64{}
	for _, p := range []struct{ company, name string }{
		{"Fox Nation", "LG"}, {"Fox Nation", "Core"}, {"Company B", "Website"}, {"Personal", "Home"},
	} {
		created, err := s.CreateProject(ctx, Project{CompanyID: &companies[p.company].ID, Name: p.name})
		if err != nil {
			return err
		}
		projects[p.name] = created.ID
	}

	at := func(h, m int) *civil.Time { t := civil.NewTime(h, m); return &t }
	day := func(offset int) *civil.Date { d := today.AddDays(offset); return &d }
	proj := func(name string) *int64 { id := projects[name]; return &id }

	for _, in := range []ActivityInput{
		// Recurring distractors. They show up on days once recurrence lands (M6).
		{Kind: schedule.KindDistractor, Title: "Kindergarten pickup", DurationMin: 40, StartAt: at(12, 10), PlannedDate: day(0), Recurrence: "weekdays"},
		{Kind: schedule.KindDistractor, Title: "Fox Nation standup", DurationMin: 15, StartAt: at(9, 30), PlannedDate: day(0), Recurrence: "weekdays", ProjectID: proj("Core")},

		// Today.
		{Title: "Review PR", DurationMin: 45, ProjectID: proj("LG"), PlannedDate: day(0), Tags: []string{"review"}},
		{Title: "Fix LG webhook retry", DurationMin: 90, ProjectID: proj("LG"), PlannedDate: day(0), Tags: []string{"backend"}, DueDate: day(3)},
		{Kind: schedule.KindDistractor, Title: "Sprint planning", DurationMin: 60, ProjectID: proj("Core"), PlannedDate: day(0), StartAt: at(14, 0)},
		{Title: "Landing page copy", DurationMin: 120, ProjectID: proj("Website"), PlannedDate: day(0)},
		{Title: "Invoice October", DurationMin: 20, ProjectID: proj("Website"), PlannedDate: day(0), DueDate: day(-2), Tags: []string{"admin"}},
		{Kind: schedule.KindDistractor, Title: "Lunch", DurationMin: 45, PlannedDate: day(0), StartAt: at(13, 0)},

		// Tomorrow and the backlog.
		{Title: "Write migration for orders", DurationMin: 60, ProjectID: proj("Core"), PlannedDate: day(1), Tags: []string{"backend"}},
		{Title: "Renew car insurance", DurationMin: 30, ProjectID: proj("Home"), DueDate: day(10)},
		{Title: "Explore .ics import", DurationMin: 90, Description: "v2 idea: import calendar events as distractors."},
	} {
		if _, err := s.CreateActivity(ctx, in); err != nil {
			return err
		}
	}
	return nil
}
