package store

import (
	"context"
	"time"

	"github.com/ocalderonc/omnitodo/internal/civil"
	"github.com/ocalderonc/omnitodo/internal/schedule"
)

type Settings struct {
	DayStart civil.Time
	DayEnd   civil.Time
	Timezone string
}

func (s Settings) Schedule() schedule.Settings {
	return schedule.Settings{DayStart: s.DayStart, DayEnd: s.DayEnd}
}

// Location loads the configured timezone, which Update has already validated.
func (s Settings) Location() (*time.Location, error) { return time.LoadLocation(s.Timezone) }

func (s *Store) GetSettings(ctx context.Context) (Settings, error) {
	var start, end string
	var out Settings
	if err := s.db.QueryRowContext(ctx, `SELECT day_start, day_end, timezone FROM settings WHERE id = 1`).
		Scan(&start, &end, &out.Timezone); err != nil {
		return Settings{}, err
	}
	var err error
	if out.DayStart, err = civil.ParseTime(start); err != nil {
		return Settings{}, err
	}
	if out.DayEnd, err = civil.ParseTime(end); err != nil {
		return Settings{}, err
	}
	return out, nil
}

func (s *Store) UpdateSettings(ctx context.Context, in Settings) error {
	if in.DayStart < 0 || in.DayEnd >= 24*60 || in.DayStart >= in.DayEnd {
		return invalid("the day must start before it ends, within 00:00–23:59")
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil || in.Timezone == "" {
		return invalid("unknown timezone %q", in.Timezone)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE settings SET day_start = ?, day_end = ?, timezone = ? WHERE id = 1`,
		in.DayStart.String(), in.DayEnd.String(), in.Timezone)
	return mapErr(err, "settings")
}
