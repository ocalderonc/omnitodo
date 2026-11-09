# OmniTodo — a to-do list on steroids

A small, self-hosted, single-user app to plan my workday across multiple companies/projects.
Tasks have durations, can be ordered manually, and are laid out on a daily timeline together with
**distractors** (meetings, kindergarten pickup, dead time) so I can see at a glance whether the day fits.
Installable on the phone as a **PWA**.

> Status: M1–M3 done, plus the settings page (companies, projects, tags, working day) and PWA and packaging from M7. M4 (drag & drop, keyboard) next. This README is the source of truth for the implementation.

---

## 1. Goals

- Track activities like a to-do list, with an optional description.
- Group activities by **project** (which belongs to a **company**) and optional free-form **tags**.
- Give each activity an **estimated duration**.
- **Manually order** activities within a day (drag & drop).
- Optional **due date** and optional **fixed start time**.
- Show the **daily total** of planned time (hours + minutes), split into work vs. distractors.
- Support **distractors**: non-work blocks that consume time (meetings, pickups, lunch, commute).
- **Installable PWA** on the phone (home-screen icon, standalone window, app shell cached).
- Free, self-hosted on Coolify, no cloud dependency, one SQLite file.

### Non-goals (for v1)
- Multi-user, sharing. (There is a single passphrase login — see §9.3 — but no accounts.)
- Native mobile apps (the PWA is enough).
- Offline *editing* (the shell loads offline; writes need the server — maybe v2).
- Actual time tracking / timers (maybe v2).
- Calendar sync (maybe v2: import `.ics` as distractors).

---

## 2. Tech stack

Chosen for: single binary, trivial to run on Coolify, easy to maintain by a backend dev.

| Layer | Choice | Why |
|---|---|---|
| Backend | **Go** (stdlib `net/http` + `chi` router) | Single binary, low maintenance |
| DB | **SQLite** (`modernc.org/sqlite`, pure Go, no CGO), WAL mode | Zero ops, one file to back up |
| Migrations | `pressly/goose` (embedded SQL files), run at startup | Simple, versioned |
| Frontend | Server-rendered `html/template` + **HTMX** + **SortableJS** | No build step, drag & drop ordering |
| PWA | `manifest.webmanifest` + hand-written `sw.js` | Installable, no toolchain |
| Styling | Plain CSS (or Pico.css) | No toolchain |
| Packaging | `Dockerfile` (distroless, nonroot) + `docker-compose.prod.yml` for Coolify | Same pattern as Moneta, minus Postgres |

All static assets embedded with `embed.FS`. No Node toolchain required.

---

## 3. Domain model

```
Company 1───* Project 1───* Activity *───* Tag
```

### Company
| field | type | notes |
|---|---|---|
| id | int PK | |
| name | text, unique | e.g. "Fox Nation", "Company B", "Personal" |
| color | text | hex, used in UI |
| archived | bool | default false |

### Project
| field | type | notes |
|---|---|---|
| id | int PK | |
| company_id | FK → company, nullable | null = no company |
| name | text | unique per company |
| color | text, nullable | falls back to company color |
| archived | bool | |

### Activity
| field | type | notes |
|---|---|---|
| id | int PK | |
| kind | enum `task` \| `distractor` | distractor = meeting, pickup, dead time |
| title | text, required | |
| description | text, nullable | markdown allowed, rendered safely |
| project_id | FK → project, nullable | |
| company_id | FK → company, nullable | only when project_id is NULL (a task for a company, no project); with a project the company comes from it. CHECK enforces at most one |
| duration_min | int, required, > 0 | estimated duration in minutes |
| planned_date | date, nullable | the day it's planned for; null = backlog |
| position | int | manual order within `planned_date` (sparse ints, e.g. steps of 1000) |
| start_at | time (HH:MM), nullable | fixed start; if null, it's "floating" |
| due_date | date, nullable | deadline (independent of planned_date) |
| status | enum `todo` \| `done` \| `skipped` | |
| recurrence | text, nullable | v1: `daily`, `weekdays`, `weekly:MON,WED` (see §6) |
| template_id | FK → activity, nullable | set on materialized recurrence instances |
| is_template | bool | recurrence template, never shown on a day directly |
| created_at / updated_at | timestamp | |

### Tag / ActivityTag
Standard many-to-many. Tag: `id`, `name` (unique), `color`.

### Settings (single row)
| field | default |
|---|---|
| day_start | `08:00` |
| day_end | `18:00` |
| timezone | `America/El_Salvador` |

---

## 4. Core logic: building the day timeline

Given a date, produce an ordered list of time blocks:

1. Take all activities planned for day D (including materialized recurrences). `skipped` ones are ignored.
2. **Anchored** items (`start_at` set) are placed at their exact time.
3. **Floating** items (`start_at` null) fill the gaps in `position` order, starting at `day_start`,
   flowing around anchored items. v1 rule: no splitting — if a floating item does not fit before the
   next anchor, **it moves after the anchor**.
4. Compute for each block its start and end.
5. Flag:
   - Two anchored items overlapping → `conflict` (both).
   - Any block ending after `day_end` → `overflow`.
   - Task with `due_date < D` still `todo` → `overdue`.
6. Gaps between blocks inside `[day_start, day_end]` are emitted as **free** blocks.

Implemented as a **pure function** in `internal/schedule` with table-driven tests. No DB, no `time.Now()`.
Times of day are `civil.Time` (minutes after midnight, wall clock) rather than `time.Time`, so no
timezone can leak into the arithmetic. Values ≥ 24:00 are allowed and mean the day overflowed.

```go
type Block struct {
    ActivityID int64      // 0 for free blocks
    Kind       Kind       // task | distractor | free
    Start, End civil.Time
    Anchored   bool
    Flags      []Flag     // conflict, overflow, overdue
}

func BuildDay(day civil.Date, items []Activity, s Settings) ([]Block, Summary)
```

### Daily summary
```
Summary {
  TaskMinutes        int  // sum of task durations (status != skipped)
  DistractorMinutes  int
  TotalMinutes       int
  AvailableMinutes   int  // day_end - day_start
  FreeMinutes        int  // Available - Total (can be negative → header turns red)
  ByCompany          map[string]Split  // Split{Task, Distractor}, Total() = both
  ByProject          map[string]Split
  DoneMinutes        int  // tasks marked done
  RemainingMinutes   int  // tasks still todo
}
```
Display as `5h 40m` (helper `FormatDuration(min int) string`).

---

## 5. UI (v1)

### Pages
- **Today / Day view** (`/day/2026-11-09`, default today)
  - Left: ordered list (drag & drop via SortableJS → `PATCH /activities/reorder`).
  - Right: vertical timeline with blocks, colored by company; distractors rendered hatched/grey.
  - Header: summary bar: `Tasks 5h 40m · Distractors 2h 10m · Free 0h 10m`, red if negative.
  - Breakdown chips per company: work only by default (`Fox Nation 4h 30m`), with the distractor time
    belonging to that company shown separately (`+30m meetings`).
  - Prev / next day, "move unfinished to tomorrow" button.
  - On phone widths the list and timeline become two tabs.
- **Backlog** (`/backlog`): activities with no `planned_date`, filter by company/project/tag, "plan for today" action.
- **Week view** (`/week`): 7 columns, daily totals only (v1.1).
- **Settings**: companies, projects, tags, day start/end.

### Quick-add syntax (single text input)
```
Fix LG webhook retry 1h30m +LG #backend @14:00 !2026-11-12
Kindergarten pickup 40m ~d @12:10 *weekdays
```
| token | meaning |
|---|---|
| `30m`, `2h`, `1h30m` | duration; if missing, 30m is used and the UI says so. If several, the last one wins and the others stay in the title |
| `+name` | project, case-insensitive (including accented letters). If two companies have a project with that name, use `+Company/Project`. With no project of that name, a company called `name` is used: the task belongs to the company without a project. `+Company/` always means the company itself. An unknown name offers to create the project. Names with spaces can't be typed here (use the form) |
| `#name` | tag (created if missing) |
| `@HH:MM` | fixed start time |
| `!YYYY-MM-DD`, `!today`, `!tomorrow`, `!mon`…`!sun` | due date (weekday = next occurrence, today counts) |
| `~d` | kind = distractor |
| `*daily`, `*weekdays`, `*weekly:MON,WED` | recurrence |
| (rest) | title |

A token that starts with a sigil but is malformed (`@25:00`, `!someday`) is an error, so typos are reported
instead of silently ending up in the title. Parser in `internal/quickadd`, pure function, table-driven tests.

### Keyboard
`n` new, `j/k` move selection, `x` toggle done, `e` edit, `t` plan for today, `←/→` change day.

### PWA
- Public URL: `https://omnitodo.ischys.fyi` (behind Cloudflare, like Moneta: enable "Always Use HTTPS",
  disable any script injection such as Web Analytics, since the CSP would block it).
- `GET /manifest.webmanifest`: name `OmniTodo`, `display: standalone`, `start_url: /day`, 192/512 px + maskable icons.
- `GET /sw.js`: generated per build. Precaches static assets (cache-first, cache named after the asset hash)
  and keeps the last copy of each day page (network-first; offline it serves that copy and the page shows an
  "offline" banner). HTMX and non-GET requests always go to the network. Served with `Cache-Control: no-cache`.
  Visiting `/login` clears the cached day pages.
- Strict CSP without `'unsafe-inline'`: `htmx.config.includeIndicatorStyles = false`, no inline scripts.

---

## 6. Recurrence (keep it minimal)

Recurring activities are **templates**. When a day is viewed, instances are materialized lazily
(create concrete rows for that date if not already present, linked via `template_id`).
This keeps ordering, done status and edits per-day without a complex RRULE engine.

- A template has `recur_from` (first day it may appear) and no `planned_date`.
- Deleting one day's instance marks it `skipped` instead, otherwise it would be recreated on the next view.
- Instances can't be moved to another day, and rollover leaves them (the next day has its own).
- Rollover also leaves distractors: a missed meeting doesn't happen tomorrow.

---

## 7. HTTP API

HTMX endpoints return HTML fragments; same handlers return JSON with `Accept: application/json`.

```
GET    /day/{date}                    day page
GET    /api/day/{date}                blocks + summary (JSON)
POST   /activities                    create (form or quick-add string)
PATCH  /activities/{id}               update fields
POST   /activities/{id}/toggle        todo <-> done
DELETE /activities/{id}
PATCH  /activities/reorder            {date, ids: [..ordered]}
POST   /day/{date}/rollover           move unfinished tasks to next day
GET    /backlog
CRUD   /companies, /projects, /tags
GET    /export.json                   full dump (backup)
POST   /import.json
GET    /login, POST /login, POST /logout
GET    /healthz                       liveness (no auth)
GET    /manifest.webmanifest, /sw.js  PWA (no auth)
```

---

## 8. Project layout

```
.
├── cmd/omnitodo/main.go       # server; `omnitodo healthcheck` subcommand for the container probe
├── internal/
│   ├── civil/               # Date and Time-of-day value types (pure)
│   ├── schedule/            # BuildDay, Summary, FormatDuration (pure, tested)
│   ├── quickadd/            # quick-add parser (pure, tested)
│   ├── store/               # SQLite repo layer
│   ├── auth/                # passphrase hash (PBKDF2), signed session cookie, login limiter
│   ├── server/              # handlers, routing, middleware (CSP, same-origin check)
│   └── recurrence/          # template expansion
├── migrations/              # goose SQL files (embedded)
├── web/                     # embedded into the binary (web/embed.go)
│   ├── templates/
│   └── static/              # htmx.min.js (2.0.4), app.css, app.js; later Sortable, icons, sw.js
├── deploy/
│   ├── Dockerfile
│   ├── docker-compose.prod.yml  # Coolify
│   └── backup.sh                # nightly encrypted backup (host cron)
├── Makefile
└── README.md
```

---

## 9. Running & deploying

### 9.1 Local
```bash
make run            # go run ./cmd/omnitodo  → http://localhost:8080
make test           # go test ./...
make build          # ./bin/omnitodo
make seed           # sample companies, projects and today's activities (empty DB only)
```

`make run` disables login (`OMNITODO_INSECURE_NO_AUTH=1`) and the cookie `Secure` flag, for local http only.
The server refuses to start without a valid passphrase hash and session secret unless that flag is set.

The binary has three subcommands: `omnitodo` / `omnitodo serve`, `omnitodo seed`, and
`omnitodo healthcheck` (probes `/healthz`; used by the container healthcheck), and
`omnitodo hash-passphrase` (reads a passphrase on stdin, prints the value for `OMNITODO_PASSPHRASE_HASH`), and
`omnitodo backup <file>` (consistent copy of the live database, see §9.4).

Config via env:
| var | default | notes |
|---|---|---|
| `OMNITODO_ADDR` | `:8080` | |
| `OMNITODO_DB` | `./data/omnitodo.db` | `/data/omnitodo.db` in the container |
| `OMNITODO_PASSPHRASE_HASH` | — (required) | `pbkdf2-sha256:…` from `omnitodo hash-passphrase` (stdlib PBKDF2, 600k iterations) |
| `OMNITODO_SESSION_SECRET` | — (required in prod) | `openssl rand -base64 48`, ≥ 32 chars |
| `OMNITODO_COOKIE_SECURE` | `true` | `false` only for local http |
| `OMNITODO_INSECURE_NO_AUTH` | unset | `1` disables login; local development only |
| `TZ` | `America/El_Salvador` | |

### 9.2 Coolify (same pattern as Moneta, much simpler)
One Coolify **Docker Compose** resource built from `deploy/docker-compose.prod.yml`. No database resource:
SQLite lives on a named volume. The image is ~18 MB (static Go binary on distroless, nonroot).

**First deploy**
1. Generate the secrets locally:
   ```bash
   go run ./cmd/omnitodo hash-passphrase     # type the passphrase, copy the pbkdf2-sha256:… line
   openssl rand -base64 48                   # session secret
   ```
2. Coolify → New resource → Docker Compose from the GitHub repo `ocalderonc/omnitodo`, branch `master`,
   compose file `/deploy/docker-compose.prod.yml`.
3. Environment variables: `OMNITODO_PASSPHRASE_HASH`, `OMNITODO_SESSION_SECRET`. The hash uses `:` separators,
   so no `$` escaping is needed.
4. Domain on the `app` service: `https://omnitodo.ischys.fyi`, port `8080`, **no path**.
5. Cloudflare: DNS record for `omnitodo` (proxied), "Always Use HTTPS" on, no script injection
   (Web Analytics / Rocket Loader): the CSP blocks it anyway.
6. Deploy, then check `https://omnitodo.ischys.fyi/healthz` answers `ok` and log in.
7. Optional sample data: `docker exec <container> /usr/local/bin/omnitodo seed` (empty database only).
8. On the phone: open the site → Share → "Add to Home Screen" (iOS) / menu → "Install app" (Android).

| Setting | Value | Why |
|---|---|---|
| Services | one: `app` | Go serves HTML, static files, PWA files and JSON |
| Ports | `expose: ["8080"]`, never `ports:` | Coolify validates the domain port against `expose` |
| Volume | named volume `omnitodo-data` → `/data` | the database; survives redeploys |
| Replicas | exactly one | SQLite allows a single writer process |
| Healthcheck | `["CMD", "/usr/local/bin/omnitodo", "healthcheck"]` | distroless has no shell/curl |
| Hardening | `read_only: true`, `cap_drop: [ALL]`, `no-new-privileges` | `/data` is the only writable path |

**Volume ownership gotcha:** the image runs as uid `65532`; a fresh volume is root-owned and SQLite then fails
with "attempt to write a readonly database". The Dockerfile creates `/data` owned by `65532:65532`, and a
*named* volume copies that ownership on first creation. A bind mount does not, so don't use one.

Migrations run inside the server at startup (goose, embedded), before it starts listening.

### 9.3 Auth
Single passphrase → HMAC-signed, `HttpOnly`, `Secure`, `SameSite=Lax` session cookie valid for a year, so the
installed PWA stays logged in. Sessions are stateless: rotating `OMNITODO_SESSION_SECRET` logs out every device.
Failed logins are limited to 10 per minute (globally: behind the proxy every client has the same address).
State-changing requests from another origin are refused (`Sec-Fetch-Site` / `Origin` check).
Basic auth is **not** used: iOS standalone PWAs don't reliably keep basic-auth credentials.

### 9.4 Backup & restore
`deploy/backup.sh` runs nightly from root's cron on the VPS (setup in the script header):
`omnitodo backup` does `VACUUM INTO` inside the running container (consistent copy, no downtime, no host
`sqlite3`), then the script copies it out, encrypts it with `age` to a public key and ships it with `rclone`.

Restore:
```bash
age -d -i key.txt omnitodo-<stamp>.db.age > omnitodo.db
# Coolify: stop the resource, then on the VPS:
docker run --rm -v <volume>:/data -v "$PWD":/in alpine sh -c \
  'rm -f /data/omnitodo.db-wal /data/omnitodo.db-shm && cp /in/omnitodo.db /data/omnitodo.db && chown 65532:65532 /data/omnitodo.db'
# start the resource again
```
`GET /export.json` (planned) will add a human-readable dump.


---

## 10. Milestones

- [x] **M1 – Core logic**: `civil`, `schedule` + `quickadd` packages with tests. No UI yet.
- [x] **M2 – Persistence**: migrations, store layer, seed data (2 companies, a few projects, kindergarten pickup distractor).
- [x] **M3 – Day view**: list + timeline + summary bar, create/edit/toggle/delete, passphrase login.
- [ ] **M4 – Ordering**: drag & drop reorder, quick-add input, keyboard shortcuts.
- [ ] **M5 – Backlog & rollover**: backlog page, plan-for-today, move unfinished to tomorrow.
- [ ] **M6 – Recurrence** for distractors (pickup on weekdays, standups).
- [x] **M7a – PWA & packaging**: manifest, service worker, icons, Dockerfile, Coolify compose, backup script.
- [ ] **M7b – Export/import**: `GET /export.json`, `POST /import.json`.
- [ ] v2 ideas: `.ics` import as distractors, actual time tracking, week view with charts, offline writes.

---

## 11. Acceptance criteria (v1)

1. I can type `Review PR 45m +LG` and it shows up in today's list with a 45-minute block.
2. I can add `Kindergarten pickup 40m ~d @12:10 *weekdays` and it appears every weekday at 12:10, greyed out.
3. Floating tasks flow around anchored items on the timeline; reordering the list updates the timeline instantly.
4. The header shows total task time, distractor time and free time; it turns red when the day is overbooked.
5. I can see per-company totals for the day (e.g. Fox Nation 4h 30m, Company B 2h).
6. Overdue tasks are visibly flagged.
7. Unfinished tasks can be rolled over to tomorrow in one click.
8. Data survives restarts/redeploys and can be exported to JSON.
9. I can install it on my phone's home screen and it opens standalone, already logged in.

---

## 12. Notes for Claude Code

- Keep `civil`, `schedule` and `quickadd` free of I/O and `time.Now()`; inject the date.
- Prefer stdlib; ask before adding a dependency not listed in §2.
- Use `position` with gaps (1000, 2000, …) and renumber only when gaps run out.
- All times stored in local wall-clock (`TZ` setting); dates as `YYYY-MM-DD` text in SQLite.
