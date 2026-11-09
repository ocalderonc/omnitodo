package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"

	"github.com/ocalderonc/omnitodo/migrations"
)

func TestNamesIgnoreCaseBeyondASCII(t *testing.T) {
	s := open(t)
	c := must[Company](t)(s.CreateCompany(ctx, Company{Name: "Éxito"}))
	for _, dup := range []string{"éxito", "ÉXITO", " éxito "} {
		if _, err := s.CreateCompany(ctx, Company{Name: dup}); !errors.Is(err, ErrConflict) {
			t.Errorf("company %q next to Éxito: %v", dup, err)
		}
	}
	other := must[Company](t)(s.CreateCompany(ctx, Company{Name: "Otra"}))
	if err := s.UpdateCompany(ctx, Company{ID: other.ID, Name: "ÉXITO"}); !errors.Is(err, ErrConflict) {
		t.Errorf("rename onto Éxito: %v", err)
	}
	if got, err := s.CompanyByName(ctx, "éXiTo"); err != nil || got.ID != c.ID {
		t.Errorf("CompanyByName: %+v %v", got, err)
	}

	p := must[Project](t)(s.CreateProject(ctx, Project{Name: "Ñandú", CompanyID: &c.ID}))
	if _, err := s.CreateProject(ctx, Project{Name: "ñANDÚ", CompanyID: &c.ID}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate project: %v", err)
	}
	// The same name under another company is still fine.
	must[Project](t)(s.CreateProject(ctx, Project{Name: "ñandú", CompanyID: &other.ID}))

	for _, ref := range []string{"éxito/ñandú", "ÉXITO/ÑANDÚ", " Éxito/Ñandú "} {
		if got, err := s.ResolveProject(ctx, ref); err != nil || got.ID != p.ID {
			t.Errorf("ResolveProject(%q): %+v %v", ref, got, err)
		}
	}
	if _, err := s.ResolveProject(ctx, "ÑANDÚ"); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("unqualified name in two companies: %v", err)
	}

	// Tags: the first spelling is kept and reused.
	day := ptr(today)
	a := must[Activity](t)(s.CreateActivity(ctx, ActivityInput{Title: "A", DurationMin: 10, PlannedDate: day, Tags: []string{"Reunión"}}))
	b := must[Activity](t)(s.CreateActivity(ctx, ActivityInput{Title: "B", DurationMin: 10, PlannedDate: day, Tags: []string{"REUNIÓN"}}))
	if a.Tags[0].ID != b.Tags[0].ID || b.Tags[0].Name != "Reunión" {
		t.Errorf("tags: %+v / %+v", a.Tags, b.Tags)
	}
}

// openAtV1 builds a database with only the first migration applied, the
// schema production had before name_key.
func openAtV1(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 1); err != nil {
		t.Fatal(err)
	}
	return path, db
}

func TestMigrationBackfillsNameKey(t *testing.T) {
	path, db := openAtV1(t)
	for _, q := range []string{
		`INSERT INTO company (id, name) VALUES (1, 'Éxito')`,
		`INSERT INTO project (company_id, name) VALUES (1, 'Ñandú')`,
		`INSERT INTO tag (name) VALUES ('Reunión')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if p, err := s.ResolveProject(ctx, "ÉXITO/ñandú"); err != nil || p.Name != "Ñandú" {
		t.Errorf("after migration: %+v %v", p, err)
	}
	if _, err := s.CreateCompany(ctx, Company{Name: "éxito"}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate after migration: %v", err)
	}
	if _, err := s.CreateProject(ctx, Project{Name: "Nuevo"}); err != nil {
		t.Errorf("create after migration: %v", err)
	}
}

func TestMigrationRefusesCaseClash(t *testing.T) {
	path, db := openAtV1(t)
	db.Exec(`INSERT INTO company (name) VALUES ('Éxito'), ('éxito')`)
	db.Close()
	_, err := Open(ctx, path)
	if err == nil || !strings.Contains(err.Error(), "Éxito / éxito") {
		t.Errorf("want a clear clash error, got %v", err)
	}
}
