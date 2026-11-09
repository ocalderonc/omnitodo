-- +goose Up
-- Dates are YYYY-MM-DD text and times of day HH:MM text, both local wall clock
-- (see README §12). The CHECKs reject anything SQLite's own date/time functions
-- would not round-trip, so a malformed value cannot be stored by any path.

CREATE TABLE company (
    id       INTEGER PRIMARY KEY,
    name     TEXT    NOT NULL UNIQUE COLLATE NOCASE CHECK (trim(name) <> ''),
    color    TEXT    NOT NULL DEFAULT '#6b7280',
    archived INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0, 1))
);

CREATE TABLE project (
    id         INTEGER PRIMARY KEY,
    company_id INTEGER REFERENCES company (id) ON DELETE SET NULL,
    name       TEXT    NOT NULL COLLATE NOCASE CHECK (trim(name) <> ''),
    color      TEXT,
    archived   INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0, 1))
);
-- UNIQUE(company_id, name) would let any number of company-less projects share
-- a name, because NULLs never compare equal. ifnull folds them into one bucket.
CREATE UNIQUE INDEX project_company_name ON project (ifnull(company_id, 0), name);

CREATE TABLE activity (
    id           INTEGER PRIMARY KEY,
    kind         TEXT    NOT NULL DEFAULT 'task' CHECK (kind IN ('task', 'distractor')),
    title        TEXT    NOT NULL CHECK (trim(title) <> ''),
    description  TEXT,
    project_id   INTEGER REFERENCES project (id) ON DELETE SET NULL,
    duration_min INTEGER NOT NULL CHECK (duration_min > 0),
    planned_date TEXT    CHECK (planned_date IS NULL OR date(planned_date) IS planned_date),
    position     INTEGER NOT NULL DEFAULT 0,
    start_at     TEXT    CHECK (start_at IS NULL OR (length(start_at) = 5 AND time(start_at) IS start_at || ':00')),
    due_date     TEXT    CHECK (due_date IS NULL OR date(due_date) IS due_date),
    status       TEXT    NOT NULL DEFAULT 'todo' CHECK (status IN ('todo', 'done', 'skipped')),

    -- Recurrence (README §6). A template is never on a day itself; viewing a
    -- day materialises one concrete row per template, linked by template_id.
    recurrence   TEXT,
    is_template  INTEGER NOT NULL DEFAULT 0 CHECK (is_template IN (0, 1)),
    recur_from   TEXT    CHECK (recur_from IS NULL OR date(recur_from) IS recur_from),
    template_id  INTEGER REFERENCES activity (id) ON DELETE SET NULL,

    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    updated_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),

    CHECK (is_template = 0 OR (recurrence IS NOT NULL AND recur_from IS NOT NULL AND planned_date IS NULL)),
    CHECK (is_template = 1 OR (recurrence IS NULL AND recur_from IS NULL))
);
CREATE INDEX activity_day ON activity (planned_date, position) WHERE is_template = 0;
CREATE INDEX activity_backlog ON activity (position) WHERE planned_date IS NULL AND is_template = 0;
-- Makes lazy materialisation idempotent: a second view of the same day, or two
-- concurrent ones, cannot create a duplicate instance.
CREATE UNIQUE INDEX activity_instance ON activity (template_id, planned_date) WHERE template_id IS NOT NULL;

CREATE TABLE tag (
    id    INTEGER PRIMARY KEY,
    name  TEXT NOT NULL UNIQUE COLLATE NOCASE CHECK (trim(name) <> ''),
    color TEXT NOT NULL DEFAULT '#6b7280'
);

CREATE TABLE activity_tag (
    activity_id INTEGER NOT NULL REFERENCES activity (id) ON DELETE CASCADE,
    tag_id      INTEGER NOT NULL REFERENCES tag (id) ON DELETE CASCADE,
    PRIMARY KEY (activity_id, tag_id)
) WITHOUT ROWID;
CREATE INDEX activity_tag_tag ON activity_tag (tag_id);

CREATE TABLE settings (
    id        INTEGER PRIMARY KEY CHECK (id = 1),
    day_start TEXT NOT NULL DEFAULT '08:00',
    day_end   TEXT NOT NULL DEFAULT '18:00',
    timezone  TEXT NOT NULL DEFAULT 'America/El_Salvador',
    CHECK (day_start < day_end)
);
INSERT INTO settings (id) VALUES (1);

-- +goose Down
DROP TABLE settings;
DROP TABLE activity_tag;
DROP TABLE tag;
DROP TABLE activity;
DROP TABLE project;
DROP TABLE company;
