package store

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
)

// Companies, projects and tags: the small lookup tables activities hang off.

type Company struct {
	ID       int64
	Name     string
	Color    string
	Archived bool
}

type Project struct {
	ID        int64
	CompanyID *int64
	Name      string
	Color     string // own color; "" falls back to the company's
	Archived  bool

	CompanyName string // read-only, joined
}

type Tag struct {
	ID    int64
	Name  string
	Color string
}

const defaultColor = "#6b7280"

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", invalid("name is required")
	}
	return name, nil
}

func cleanColor(c string, allowEmpty bool) (string, error) {
	switch {
	case c == "" && allowEmpty:
		return "", nil
	case c == "":
		return defaultColor, nil
	case !hexColor.MatchString(c):
		return "", invalid("color %q: want #rrggbb", c)
	}
	return strings.ToLower(c), nil
}

// --- companies ----------------------------------------------------------------

func (s *Store) CreateCompany(ctx context.Context, c Company) (Company, error) {
	var err error
	if c.Name, err = cleanName(c.Name); err != nil {
		return Company{}, err
	}
	if c.Color, err = cleanColor(c.Color, false); err != nil {
		return Company{}, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO company (name, color, archived) VALUES (?, ?, ?)`, c.Name, c.Color, c.Archived)
	if err != nil {
		return Company{}, mapErr(err, "company "+c.Name)
	}
	c.ID, _ = res.LastInsertId()
	return c, nil
}

func (s *Store) UpdateCompany(ctx context.Context, c Company) error {
	var err error
	if c.Name, err = cleanName(c.Name); err != nil {
		return err
	}
	if c.Color, err = cleanColor(c.Color, false); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE company SET name = ?, color = ?, archived = ? WHERE id = ?`, c.Name, c.Color, c.Archived, c.ID)
	return affected(res, mapErr(err, "company "+c.Name))
}

// DeleteCompany keeps its projects, which become company-less.
func (s *Store) DeleteCompany(ctx context.Context, id int64) error {
	return affected(s.db.ExecContext(ctx, `DELETE FROM company WHERE id = ?`, id))
}

func (s *Store) ListCompanies(ctx context.Context, includeArchived bool) ([]Company, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, color, archived FROM company
		WHERE archived = 0 OR ? ORDER BY name`, includeArchived)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Company
	for rows.Next() {
		var c Company
		if err := rows.Scan(&c.ID, &c.Name, &c.Color, &c.Archived); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// --- projects -----------------------------------------------------------------

func (s *Store) CreateProject(ctx context.Context, p Project) (Project, error) {
	var err error
	if p.Name, err = cleanName(p.Name); err != nil {
		return Project{}, err
	}
	if p.Color, err = cleanColor(p.Color, true); err != nil {
		return Project{}, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO project (company_id, name, color, archived) VALUES (?, ?, ?, ?)`,
		nullInt(p.CompanyID), p.Name, nullString(p.Color), p.Archived)
	if err != nil {
		return Project{}, mapErr(err, "project "+p.Name)
	}
	p.ID, _ = res.LastInsertId()
	return p, nil
}

func (s *Store) UpdateProject(ctx context.Context, p Project) error {
	var err error
	if p.Name, err = cleanName(p.Name); err != nil {
		return err
	}
	if p.Color, err = cleanColor(p.Color, true); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE project SET company_id = ?, name = ?, color = ?, archived = ? WHERE id = ?`,
		nullInt(p.CompanyID), p.Name, nullString(p.Color), p.Archived, p.ID)
	return affected(res, mapErr(err, "project "+p.Name))
}

// DeleteProject keeps its activities, which become project-less.
func (s *Store) DeleteProject(ctx context.Context, id int64) error {
	return affected(s.db.ExecContext(ctx, `DELETE FROM project WHERE id = ?`, id))
}

const projectCols = `p.id, p.company_id, p.name, ifnull(p.color, ''), p.archived, ifnull(c.name, '')
	FROM project p LEFT JOIN company c ON c.id = p.company_id`

func scanProjects(rows *sql.Rows, err error) ([]Project, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		var cid sql.NullInt64
		if err := rows.Scan(&p.ID, &cid, &p.Name, &p.Color, &p.Archived, &p.CompanyName); err != nil {
			return nil, err
		}
		p.CompanyID = scanInt(cid)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) ListProjects(ctx context.Context, includeArchived bool) ([]Project, error) {
	return scanProjects(s.db.QueryContext(ctx, `SELECT `+projectCols+`
		WHERE p.archived = 0 OR ? ORDER BY ifnull(c.name, ''), p.name`, includeArchived))
}

// ResolveProject finds an active project by name, case-insensitively, as the
// quick-add "+name" token needs. "LG" matches a project called "lg" in any
// company; if two companies both have one, the caller gets ErrAmbiguous and
// should ask which. "Company/Project" picks one explicitly.
func (s *Store) ResolveProject(ctx context.Context, name string) (Project, error) {
	company, project, qualified := strings.Cut(strings.TrimSpace(name), "/")
	if !qualified {
		project, company = company, ""
	}
	ps, err := scanProjects(s.db.QueryContext(ctx, `SELECT `+projectCols+`
		WHERE p.archived = 0 AND p.name = ? COLLATE NOCASE
		  AND (? = '' OR c.name = ? COLLATE NOCASE)`, project, company, company))
	switch {
	case err != nil:
		return Project{}, err
	case len(ps) == 0:
		return Project{}, ErrNotFound
	case len(ps) > 1:
		return Project{}, ErrAmbiguous
	}
	return ps[0], nil
}

// --- tags ---------------------------------------------------------------------

func (s *Store) ListTags(ctx context.Context) ([]Tag, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, color FROM tag ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.Color); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) UpdateTag(ctx context.Context, t Tag) error {
	var err error
	if t.Name, err = cleanName(t.Name); err != nil {
		return err
	}
	if t.Color, err = cleanColor(t.Color, false); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE tag SET name = ?, color = ? WHERE id = ?`, t.Name, t.Color, t.ID)
	return affected(res, mapErr(err, "tag "+t.Name))
}

func (s *Store) DeleteTag(ctx context.Context, id int64) error {
	return affected(s.db.ExecContext(ctx, `DELETE FROM tag WHERE id = ?`, id))
}

// ensureTags returns the ids for names, creating missing tags. Matching is
// case-insensitive and the first spelling ever used is kept.
func ensureTags(ctx context.Context, tx *sql.Tx, names []string) ([]int64, error) {
	var ids []int64
	seen := map[int64]bool{}
	for _, n := range names {
		n, err := cleanName(strings.TrimPrefix(n, "#"))
		if err != nil {
			return nil, err
		}
		var id int64
		err = tx.QueryRowContext(ctx, `INSERT INTO tag (name) VALUES (?)
			ON CONFLICT (name) DO UPDATE SET name = name RETURNING id`, n).Scan(&id)
		if err != nil {
			return nil, err
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}
