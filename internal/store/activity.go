package store

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"

	"github.com/ocalderonc/omnitodo/internal/civil"
	"github.com/ocalderonc/omnitodo/internal/recurrence"
	"github.com/ocalderonc/omnitodo/internal/schedule"
)

const positionStep = 1000

type Activity struct {
	ID          int64
	Kind        schedule.Kind
	Title       string
	Description string
	ProjectID   *int64
	CompanyID   *int64 // set only when there is no project; see CompanyName
	DurationMin int
	PlannedDate *civil.Date // nil = backlog (or a template)
	Position    int
	StartAt     *civil.Time
	DueDate     *civil.Date
	Status      schedule.Status
	Recurrence  string      // templates only
	IsTemplate  bool        //
	RecurFrom   *civil.Date // templates only: first day it may appear
	TemplateID  *int64      // set on materialised instances
	Tags        []Tag
	CreatedAt   time.Time
	UpdatedAt   time.Time

	// Read-only, joined for display.
	ProjectName string
	CompanyName string // the project's company, or the activity's own
	Color       string // project color, else company color, else ""
}

// ScheduleItem is the view of a for schedule.BuildDay.
func (a Activity) ScheduleItem() schedule.Activity {
	return schedule.Activity{
		ID: a.ID, Kind: a.Kind, Status: a.Status, DurationMin: a.DurationMin, Position: a.Position,
		StartAt: a.StartAt, DueDate: a.DueDate, Company: a.CompanyName, Project: a.ProjectName,
	}
}

// ActivityInput holds the editable fields of an activity.
type ActivityInput struct {
	Kind        schedule.Kind // "" means task
	Title       string
	Description string
	ProjectID   *int64
	CompanyID   *int64 // only without a project: ignored when ProjectID is set
	DurationMin int
	PlannedDate *civil.Date // on create: the day, or nil for backlog. With Recurrence: the first day.
	StartAt     *civil.Time
	DueDate     *civil.Date
	Recurrence  string   // non-empty makes a template; see recurrence.Parse
	Tags        []string // names, created when missing
}

func (in *ActivityInput) validate() error {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return invalid("title is required")
	}
	if in.ProjectID != nil {
		in.CompanyID = nil // the project decides the company
	}
	if in.DurationMin <= 0 {
		return invalid("duration must be positive")
	}
	switch in.Kind {
	case "":
		in.Kind = schedule.KindTask
	case schedule.KindTask, schedule.KindDistractor:
	default:
		return invalid("kind %q", in.Kind)
	}
	if in.StartAt != nil && (*in.StartAt < 0 || *in.StartAt >= 24*60) {
		return invalid("start time %s", in.StartAt)
	}
	if in.Recurrence != "" {
		r, err := recurrence.Parse(in.Recurrence)
		if err != nil {
			return invalid("%v", err)
		}
		in.Recurrence = r.String()
	}
	return nil
}

// --- reads ------------------------------------------------------------------

const activitySelect = `SELECT a.id, a.kind, a.title, ifnull(a.description, ''), a.project_id, a.duration_min,
	a.planned_date, a.position, a.start_at, a.due_date, a.status, ifnull(a.recurrence, ''), a.is_template,
	a.recur_from, a.template_id, a.created_at, a.updated_at, a.company_id,
	ifnull(p.name, ''), ifnull(c.name, ''), coalesce(p.color, c.color, '')
	FROM activity a
	LEFT JOIN project p ON p.id = a.project_id
	LEFT JOIN company c ON c.id = CASE WHEN a.project_id IS NULL THEN a.company_id ELSE p.company_id END`

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func queryActivities(ctx context.Context, q querier, where string, args ...any) ([]Activity, error) {
	rows, err := q.QueryContext(ctx, activitySelect+" "+where, args...)
	if err != nil {
		return nil, err
	}
	var out []Activity
	for rows.Next() {
		var a Activity
		var projectID, templateID, companyID sql.NullInt64
		var planned, start, due, recurFrom sql.NullString
		var created, updated string
		if err := rows.Scan(&a.ID, &a.Kind, &a.Title, &a.Description, &projectID, &a.DurationMin,
			&planned, &a.Position, &start, &due, &a.Status, &a.Recurrence, &a.IsTemplate,
			&recurFrom, &templateID, &created, &updated, &companyID,
			&a.ProjectName, &a.CompanyName, &a.Color); err != nil {
			rows.Close()
			return nil, err
		}
		a.ProjectID, a.TemplateID, a.CompanyID = scanInt(projectID), scanInt(templateID), scanInt(companyID)
		if a.PlannedDate, err = scanDate(planned); err == nil {
			if a.DueDate, err = scanDate(due); err == nil {
				if a.RecurFrom, err = scanDate(recurFrom); err == nil {
					a.StartAt, err = scanTime(start)
				}
			}
		}
		if err != nil {
			rows.Close()
			return nil, err
		}
		a.CreatedAt, _ = time.Parse(time.RFC3339, created)
		a.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		out = append(out, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, loadTags(ctx, q, out)
}

func loadTags(ctx context.Context, q querier, as []Activity) error {
	if len(as) == 0 {
		return nil
	}
	ids := make([]any, len(as))
	byID := make(map[int64]*Activity, len(as))
	for i := range as {
		ids[i] = as[i].ID
		byID[as[i].ID] = &as[i]
	}
	rows, err := q.QueryContext(ctx, `SELECT at.activity_id, t.id, t.name, t.color
		FROM activity_tag at JOIN tag t ON t.id = at.tag_id
		WHERE at.activity_id IN (`+placeholders(len(ids))+`) ORDER BY t.name`, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var aid int64
		var t Tag
		if err := rows.Scan(&aid, &t.ID, &t.Name, &t.Color); err != nil {
			return err
		}
		byID[aid].Tags = append(byID[aid].Tags, t)
	}
	return rows.Err()
}

func (s *Store) GetActivity(ctx context.Context, id int64) (Activity, error) {
	as, err := queryActivities(ctx, s.db, `WHERE a.id = ?`, id)
	if err != nil {
		return Activity{}, err
	}
	if len(as) == 0 {
		return Activity{}, ErrNotFound
	}
	return as[0], nil
}

// ListDay returns the concrete activities planned for d, in manual order.
// Skipped ones are included so the UI can show and restore them.
func (s *Store) ListDay(ctx context.Context, d civil.Date) ([]Activity, error) {
	return queryActivities(ctx, s.db, `WHERE a.is_template = 0 AND a.planned_date = ?
		ORDER BY a.position, a.id`, d.String())
}

type BacklogFilter struct {
	CompanyID, ProjectID, TagID *int64
}

// ListBacklog returns unplanned, unfinished activities in manual order.
func (s *Store) ListBacklog(ctx context.Context, f BacklogFilter) ([]Activity, error) {
	return queryActivities(ctx, s.db, `WHERE a.is_template = 0 AND a.planned_date IS NULL AND a.status = 'todo'
		AND (? IS NULL OR c.id = ?)
		AND (? IS NULL OR a.project_id = ?)
		AND (? IS NULL OR EXISTS (SELECT 1 FROM activity_tag at WHERE at.activity_id = a.id AND at.tag_id = ?))
		ORDER BY a.position, a.id`,
		nullInt(f.CompanyID), nullInt(f.CompanyID), nullInt(f.ProjectID), nullInt(f.ProjectID), nullInt(f.TagID), nullInt(f.TagID))
}

func (s *Store) ListTemplates(ctx context.Context) ([]Activity, error) {
	return queryActivities(ctx, s.db, `WHERE a.is_template = 1 ORDER BY a.start_at, a.title`)
}

// --- writes -----------------------------------------------------------------

// CreateActivity adds an activity at the end of its day (or of the backlog).
// With a Recurrence it creates a template starting on PlannedDate instead,
// which is then required.
func (s *Store) CreateActivity(ctx context.Context, in ActivityInput) (Activity, error) {
	if err := in.validate(); err != nil {
		return Activity{}, err
	}
	template := in.Recurrence != ""
	if template && in.PlannedDate == nil {
		return Activity{}, invalid("a recurring activity needs the day it starts")
	}
	var id int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		planned, recurFrom, pos := nullDate(in.PlannedDate), any(nil), 0
		if template {
			planned, recurFrom = nil, in.PlannedDate.String()
		} else {
			var err error
			if pos, err = nextPosition(ctx, tx, in.PlannedDate); err != nil {
				return err
			}
		}
		err := tx.QueryRowContext(ctx, `INSERT INTO activity
			(kind, title, description, project_id, company_id, duration_min, planned_date, position, start_at, due_date,
			 recurrence, is_template, recur_from)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
			in.Kind, in.Title, nullString(in.Description), nullInt(in.ProjectID), nullInt(in.CompanyID), in.DurationMin, planned, pos,
			nullTime(in.StartAt), nullDate(in.DueDate), nullString(in.Recurrence), template, recurFrom).Scan(&id)
		if err != nil {
			return mapErr(err, "activity")
		}
		return setTags(ctx, tx, id, in.Tags)
	})
	if err != nil {
		return Activity{}, err
	}
	return s.GetActivity(ctx, id)
}

// UpdateActivity replaces the editable fields. The day is not changed here
// (use MoveToDate, which also fixes the position), and neither is whether the
// activity is a template: a template's Recurrence may change, but setting one
// on a concrete activity, or clearing a template's, is rejected.
func (s *Store) UpdateActivity(ctx context.Context, id int64, in ActivityInput) (Activity, error) {
	if err := in.validate(); err != nil {
		return Activity{}, err
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var isTemplate bool
		if err := tx.QueryRowContext(ctx, `SELECT is_template FROM activity WHERE id = ?`, id).Scan(&isTemplate); err != nil {
			if err == sql.ErrNoRows {
				return ErrNotFound
			}
			return err
		}
		if isTemplate != (in.Recurrence != "") {
			return invalid("cannot add or remove recurrence on an existing activity")
		}
		_, err := tx.ExecContext(ctx, `UPDATE activity SET kind = ?, title = ?, description = ?, project_id = ?,
			company_id = ?, duration_min = ?, start_at = ?, due_date = ?, recurrence = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ?`,
			in.Kind, in.Title, nullString(in.Description), nullInt(in.ProjectID), nullInt(in.CompanyID), in.DurationMin,
			nullTime(in.StartAt), nullDate(in.DueDate), nullString(in.Recurrence), id)
		if err != nil {
			return mapErr(err, "activity")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM activity_tag WHERE activity_id = ?`, id); err != nil {
			return err
		}
		return setTags(ctx, tx, id, in.Tags)
	})
	if err != nil {
		return Activity{}, err
	}
	return s.GetActivity(ctx, id)
}

func setTags(ctx context.Context, tx *sql.Tx, id int64, names []string) error {
	tagIDs, err := ensureTags(ctx, tx, names)
	if err != nil {
		return err
	}
	for _, tid := range tagIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO activity_tag (activity_id, tag_id) VALUES (?, ?)`, id, tid); err != nil {
			return err
		}
	}
	return nil
}

// DeleteActivity removes an activity. A materialised recurrence instance is
// marked skipped instead: deleting it would only make it reappear the next
// time its day is viewed. Deleting a template keeps its past instances.
func (s *Store) DeleteActivity(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE activity SET status = 'skipped',
		updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ? AND template_id IS NOT NULL`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	return affected(s.db.ExecContext(ctx, `DELETE FROM activity WHERE id = ?`, id))
}

func (s *Store) SetStatus(ctx context.Context, id int64, st schedule.Status) error {
	switch st {
	case schedule.StatusTodo, schedule.StatusDone, schedule.StatusSkipped:
	default:
		return invalid("status %q", st)
	}
	return affected(s.db.ExecContext(ctx, `UPDATE activity SET status = ?,
		updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ? AND is_template = 0`, st, id))
}

// ToggleDone flips todo <-> done (skipped becomes done) and returns the new status.
func (s *Store) ToggleDone(ctx context.Context, id int64) (schedule.Status, error) {
	var st schedule.Status
	err := s.db.QueryRowContext(ctx, `UPDATE activity
		SET status = CASE status WHEN 'done' THEN 'todo' ELSE 'done' END,
		    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
		WHERE id = ? AND is_template = 0 RETURNING status`, id).Scan(&st)
	if err == sql.ErrNoRows {
		return "", ErrNotFound
	}
	return st, err
}

// --- ordering and moving -----------------------------------------------------

// nextPosition is one step past the last position on day d (nil = backlog).
func nextPosition(ctx context.Context, tx *sql.Tx, d *civil.Date) (int, error) {
	var maxPos int
	err := tx.QueryRowContext(ctx, `SELECT ifnull(max(position), 0) FROM activity
		WHERE is_template = 0 AND planned_date IS ?`, nullDate(d)).Scan(&maxPos)
	return maxPos + positionStep, err
}

// Reorder sets the manual order of day d (nil = backlog) to ids. Every id must
// belong to that list; any of the list's activities not named (e.g. added in
// another tab since the page loaded) keep their relative order after them.
// Positions are renumbered in steps of positionStep.
func (s *Store) Reorder(ctx context.Context, d *civil.Date, ids []int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM activity WHERE is_template = 0 AND planned_date IS ?
			ORDER BY position, id`, nullDate(d))
		if err != nil {
			return err
		}
		var current []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			current = append(current, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		order := make([]int64, 0, len(current))
		named := map[int64]bool{}
		for _, id := range ids {
			if named[id] || !slices.Contains(current, id) {
				return invalid("activity %d is not in this list, or is listed twice", id)
			}
			named[id] = true
			order = append(order, id)
		}
		for _, id := range current {
			if !named[id] {
				order = append(order, id)
			}
		}
		for i, id := range order {
			if _, err := tx.ExecContext(ctx, `UPDATE activity SET position = ? WHERE id = ?`, (i+1)*positionStep, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// MoveToDate plans an activity for day d (nil = back to the backlog), at the
// end of that list. Recurrence instances cannot move: their template would
// recreate them on the original day.
func (s *Store) MoveToDate(ctx context.Context, id int64, d *civil.Date) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var templateID sql.NullInt64
		var isTemplate bool
		err := tx.QueryRowContext(ctx, `SELECT template_id, is_template FROM activity WHERE id = ?`, id).Scan(&templateID, &isTemplate)
		switch {
		case err == sql.ErrNoRows:
			return ErrNotFound
		case err != nil:
			return err
		case isTemplate || templateID.Valid:
			return invalid("recurring activities cannot be moved to another day")
		}
		pos, err := nextPosition(ctx, tx, d)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE activity SET planned_date = ?, position = ?,
			updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ?`, nullDate(d), pos, id)
		return err
	})
}

// Rollover moves the unfinished tasks of day from to the end of day to, keeping
// their order, and returns how many moved. Distractors stay (a missed meeting
// does not happen tomorrow), and so do recurrence instances (tomorrow gets its
// own). Fixed start times are kept.
func (s *Store) Rollover(ctx context.Context, from, to civil.Date) (int, error) {
	var moved int
	err := s.tx(ctx, func(tx *sql.Tx) error {
		pos, err := nextPosition(ctx, tx, &to)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM activity
			WHERE is_template = 0 AND planned_date = ? AND kind = 'task' AND status = 'todo' AND template_id IS NULL
			ORDER BY position, id`, from.String())
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for i, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE activity SET planned_date = ?, position = ?,
				updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ?`,
				to.String(), pos+i*positionStep, id); err != nil {
				return err
			}
		}
		moved = len(ids)
		return nil
	})
	return moved, err
}
