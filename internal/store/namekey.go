package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Names of companies, projects and tags are unique and looked up ignoring
// case. SQLite's NOCASE only folds ASCII, so "Éxito" and "éxito" would be two
// companies; instead every row carries name_key, the name lowercased by Go's
// Unicode rules, and uniqueness and lookups go through it.
func nameKey(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// migrationNameKey is migration 2. It is written in Go because the backfill
// needs nameKey; SQLite's lower() is ASCII-only too.
func migrationNameKey(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"company", "project", "tag"} {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN name_key TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT id, name FROM `+table)
		if err != nil {
			return err
		}
		keys := map[int64]string{}
		for rows.Next() {
			var id int64
			var name string
			if err := rows.Scan(&id, &name); err != nil {
				rows.Close()
				return err
			}
			keys[id] = nameKey(name)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for id, key := range keys {
			if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET name_key = ? WHERE id = ?`, key, id); err != nil {
				return err
			}
		}
	}

	// Rows that only differed by the case of a non-ASCII letter now clash.
	// Say which, rather than failing on the index with a bare constraint error.
	for _, q := range []struct{ what, sql string }{
		{"companies", `SELECT group_concat(name, ' / ') FROM company GROUP BY name_key HAVING count(*) > 1`},
		{"projects", `SELECT group_concat(name, ' / ') FROM project GROUP BY ifnull(company_id, 0), name_key HAVING count(*) > 1`},
		{"tags", `SELECT group_concat(name, ' / ') FROM tag GROUP BY name_key HAVING count(*) > 1`},
	} {
		var dup string
		err := tx.QueryRowContext(ctx, q.sql).Scan(&dup)
		if err == nil {
			return fmt.Errorf("%s differ only by letter case (%s): rename one with an older version, then upgrade", q.what, dup)
		} else if err != sql.ErrNoRows {
			return err
		}
	}

	_, err := tx.ExecContext(ctx, `
		CREATE UNIQUE INDEX company_name_key ON company (name_key);
		CREATE UNIQUE INDEX project_company_name_key ON project (ifnull(company_id, 0), name_key);
		CREATE UNIQUE INDEX tag_name_key ON tag (name_key);`)
	return err
}
