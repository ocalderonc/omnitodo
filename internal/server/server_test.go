package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ocalderonc/omnitodo/internal/auth"
	"github.com/ocalderonc/omnitodo/internal/civil"
	"github.com/ocalderonc/omnitodo/internal/schedule"
	"github.com/ocalderonc/omnitodo/internal/store"
)

const passphrase = "correct horse battery"

var (
	ctx   = context.Background()
	today = civil.NewDate(2026, 11, 9)
	// 10:30 in El Salvador (UTC-6).
	now = time.Date(2026, 11, 9, 16, 30, 0, 0, time.UTC)
)

type env struct {
	t      *testing.T
	store  *store.Store
	srv    *httptest.Server
	cookie *http.Cookie
}

var hashOnce = func() func() string {
	var h string
	return func() string {
		if h == "" {
			h, _ = auth.HashPassphrase(passphrase)
		}
		return h
	}
}()

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sessions, _ := auth.NewSessions(strings.Repeat("k", 40), time.Hour)
	h, err := New(st, Config{PassphraseHash: hashOnce(), Sessions: sessions, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &env{t: t, store: st, srv: srv}
}

type resp struct {
	status int
	header http.Header
	body   string
}

func (e *env) do(method, path string, form url.Values, hx bool) resp {
	e.t.Helper()
	var body io.Reader
	if form != nil && method != http.MethodGet && method != http.MethodDelete {
		body = strings.NewReader(form.Encode())
	} else if form != nil {
		path += "?" + form.Encode()
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, body)
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if method != http.MethodGet {
		req.Header.Set("Origin", e.srv.URL)
	}
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	if e.cookie != nil {
		req.AddCookie(e.cookie)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return resp{r.StatusCode, r.Header, string(b)}
}

func (e *env) login() {
	e.t.Helper()
	r := e.do(http.MethodPost, "/login", url.Values{"passphrase": {passphrase}}, false)
	if r.status != http.StatusSeeOther {
		e.t.Fatalf("login: %d %s", r.status, r.body)
	}
	for _, c := range (&http.Response{Header: r.header}).Cookies() {
		if c.Name == sessionCookie {
			e.cookie = c
		}
	}
	if e.cookie == nil || !e.cookie.HttpOnly || e.cookie.SameSite != http.SameSiteLaxMode {
		e.t.Fatalf("session cookie: %+v", e.cookie)
	}
}

func contains(t *testing.T, what, body string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("%s does not contain %q", what, w)
		}
	}
}

func TestAuth(t *testing.T) {
	e := newEnv(t)

	if r := e.do(http.MethodGet, "/healthz", nil, false); r.status != 200 {
		t.Errorf("healthz without login: %d", r.status)
	}
	r := e.do(http.MethodGet, "/day/2026-11-09", nil, false)
	if r.status != http.StatusSeeOther || r.header.Get("Location") != "/login?next=%2Fday%2F2026-11-09" {
		t.Errorf("anonymous page: %d %s", r.status, r.header.Get("Location"))
	}
	r = e.do(http.MethodPost, "/activities/1/toggle", nil, true)
	if r.status != http.StatusUnauthorized || r.header.Get("HX-Redirect") != "/login" {
		t.Errorf("anonymous htmx: %d %v", r.status, r.header)
	}

	r = e.do(http.MethodPost, "/login", url.Values{"passphrase": {"nope"}}, false)
	if r.status != http.StatusUnauthorized {
		t.Errorf("wrong passphrase: %d", r.status)
	}
	contains(t, "failed login page", r.body, "Wrong passphrase")

	// next= must not redirect off-site.
	r = e.do(http.MethodPost, "/login", url.Values{"passphrase": {passphrase}, "next": {"//evil.example"}}, false)
	if r.header.Get("Location") != "/day" {
		t.Errorf("open redirect: %q", r.header.Get("Location"))
	}
	e.login()
	if r := e.do(http.MethodGet, "/day/2026-11-09", nil, false); r.status != 200 {
		t.Errorf("logged-in page: %d", r.status)
	}
	e.cookie.Value = "1999999999.forged"
	if r := e.do(http.MethodGet, "/day/2026-11-09", nil, false); r.status != http.StatusSeeOther {
		t.Errorf("forged cookie: %d", r.status)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t)
	for range 10 {
		e.do(http.MethodPost, "/login", url.Values{"passphrase": {"wrong"}}, false)
	}
	r := e.do(http.MethodPost, "/login", url.Values{"passphrase": {passphrase}}, false)
	if r.status != http.StatusTooManyRequests {
		t.Errorf("11th attempt: %d", r.status)
	}
}

func TestCrossOriginRefused(t *testing.T) {
	e := newEnv(t)
	e.login()
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/activities", strings.NewReader("q=Evil+10m"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	req.AddCookie(e.cookie)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin post: %d", r.StatusCode)
	}
	req.Header.Del("Origin")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Body = io.NopCloser(strings.NewReader("q=Evil+10m"))
	r, _ = http.DefaultClient.Do(req)
	r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site post: %d", r.StatusCode)
	}
	if acts, _ := e.store.ListDay(ctx, today); len(acts) != 0 {
		t.Error("a cross-origin request created an activity")
	}
}

func TestDayPage(t *testing.T) {
	e := newEnv(t)
	e.login()
	if err := e.store.Seed(ctx, today); err != nil {
		t.Fatal(err)
	}
	r := e.do(http.MethodGet, "/day", nil, false)
	if r.status != 200 {
		t.Fatalf("day: %d", r.status)
	}
	contains(t, "day page", r.body,
		"Monday, 9 November 2026", `href="/day/2026-11-08"`, `href="/day/2026-11-10"`,
		"Tasks <b>4h 35m</b>", "Review PR", "Fox Nation", "overdue", `class="now"`,
		"/static/app.css?v=")
	if r.header.Get("Content-Security-Policy") == "" {
		t.Error("no CSP")
	}
	// Another day: no "now" line, and a link back to today.
	r = e.do(http.MethodGet, "/day/2026-11-10", nil, false)
	contains(t, "tomorrow", r.body, "Write migration for orders", `href="/day">Today`)
	if strings.Contains(r.body, `class="now"`) {
		t.Error("now line on another day")
	}
	if r := e.do(http.MethodGet, "/day/2026-13-01", nil, false); r.status != http.StatusBadRequest {
		t.Errorf("bad date: %d", r.status)
	}
}

func TestQuickAdd(t *testing.T) {
	e := newEnv(t)
	e.login()
	fox, _ := e.store.CreateCompany(ctx, store.Company{Name: "Fox Nation", Color: "#ea580c"})
	e.store.CreateProject(ctx, store.Project{CompanyID: &fox.ID, Name: "LG"})

	r := e.do(http.MethodPost, "/activities", url.Values{"q": {"Review PR 45m +lg #review"}, "day": {"2026-11-09"}}, true)
	if r.status != 200 {
		t.Fatalf("quick add: %d %s", r.status, r.body)
	}
	contains(t, "fragment", r.body, `id="day"`, "Added “Review PR”", "08:00–08:45", "#review", "Fox Nation")
	if strings.Contains(r.body, "<html") {
		t.Error("HTMX request got a full page")
	}
	if r.header.Get("HX-Trigger") != "closeModal" {
		t.Error("no closeModal trigger")
	}

	r = e.do(http.MethodPost, "/activities", url.Values{"q": {"Think"}, "day": {"2026-11-09"}}, true)
	contains(t, "default duration", r.body, "with the default 30m")

	for q, msg := range map[string]string{
		"Call 10m @25:00": "start time @25:00",
		"Call 10m +nope":  "no project called “nope”",
	} {
		r = e.do(http.MethodPost, "/activities", url.Values{"q": {q}, "day": {"2026-11-09"}}, true)
		contains(t, q, r.body, msg, `aria-invalid="true"`)
	}
	r = e.do(http.MethodPost, "/activities", url.Values{"q": {"Call 10m +nope"}, "day": {"2026-11-09"}}, true)
	contains(t, "input kept after an error", r.body, `value="Call 10m &#43;nope"`)

	acts, _ := e.store.ListDay(ctx, today)
	if len(acts) != 2 || acts[0].ProjectName != "LG" || acts[1].DurationMin != 30 {
		t.Errorf("stored: %+v", acts)
	}

	// Without HTMX the same post redirects back to the day.
	r = e.do(http.MethodPost, "/activities", url.Values{"q": {"Plain 5m"}, "day": {"2026-11-09"}}, false)
	if r.status != http.StatusSeeOther || r.header.Get("Location") != "/day/2026-11-09" {
		t.Errorf("non-htmx post: %d %s", r.status, r.header.Get("Location"))
	}
}

func TestFormEditToggleDelete(t *testing.T) {
	e := newEnv(t)
	e.login()

	r := e.do(http.MethodGet, "/activities/new", url.Values{"day": {"2026-11-09"}}, true)
	contains(t, "new form", r.body, `hx-post="/activities"`, `value="30m"`, `name="day" value="2026-11-09"`)

	form := url.Values{"form": {"1"}, "day": {"2026-11-09"}, "title": {"Standup"}, "kind": {"distractor"},
		"duration": {"15"}, "start_at": {"09:30"}, "tags": {"#team, sync"}}
	r = e.do(http.MethodPost, "/activities", form, true)
	contains(t, "after form create", r.body, "Saved “Standup”", "09:30–09:45")
	acts, _ := e.store.ListDay(ctx, today)
	if len(acts) != 1 || acts[0].Kind != schedule.KindDistractor || len(acts[0].Tags) != 2 {
		t.Fatalf("form create stored: %+v", acts)
	}
	id := acts[0].ID
	path := "/activities/" + itoa(id)

	r = e.do(http.MethodGet, path+"/edit", url.Values{"day": {"2026-11-09"}}, true)
	contains(t, "edit form", r.body, `hx-patch="`+path+`"`, `value="Standup"`, `value="15m"`, `value="09:30"`, `value="sync, team"`)

	form.Set("duration", "2 hours")
	r = e.do(http.MethodPatch, path, form, true)
	if r.header.Get("HX-Retarget") != "#modal-body" {
		t.Errorf("invalid edit not retargeted to the dialog: %v", r.header)
	}
	contains(t, "invalid edit", r.body, "write it like 45m", `value="Standup"`)

	form.Set("duration", "1h 15m")
	form.Set("title", "Long standup")
	form.Set("start_at", "")
	r = e.do(http.MethodPatch, path, form, true)
	contains(t, "valid edit", r.body, "Saved “Long standup”", "08:00–09:15")

	r = e.do(http.MethodPost, path+"/toggle", url.Values{"day": {"2026-11-09"}}, true)
	contains(t, "toggled", r.body, `aria-pressed="true"`)
	a, _ := e.store.GetActivity(ctx, id)
	if a.Status != schedule.StatusDone {
		t.Errorf("status after toggle = %s", a.Status)
	}

	r = e.do(http.MethodDelete, path, url.Values{"day": {"2026-11-09"}}, true)
	contains(t, "deleted", r.body, "Deleted “Long standup”", "Nothing planned yet")
	if r := e.do(http.MethodDelete, path, url.Values{"day": {"2026-11-09"}}, true); r.status != http.StatusNotFound {
		t.Errorf("second delete: %d", r.status)
	}
}

func TestDayJSON(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.store.Seed(ctx, today)
	r := e.do(http.MethodGet, "/api/day/2026-11-09", nil, false)
	if r.status != 200 {
		t.Fatalf("json: %d", r.status)
	}
	var out jsonDay
	if err := json.Unmarshal([]byte(r.body), &out); err != nil {
		t.Fatal(err)
	}
	if out.Summary.TaskMinutes != 275 || out.Summary.ByCompany["Fox Nation"].Task != 135 || len(out.Blocks) == 0 {
		t.Errorf("json day: %+v", out.Summary)
	}
	if out.Blocks[0].Start != "08:00" {
		t.Errorf("first block starts %s", out.Blocks[0].Start)
	}
}

func TestStaticCaching(t *testing.T) {
	e := newEnv(t)
	r := e.do(http.MethodGet, "/static/app.js", nil, false)
	if r.status != 200 || r.header.Get("Cache-Control") != "no-cache" {
		t.Errorf("unversioned asset: %d %q", r.status, r.header.Get("Cache-Control"))
	}
	r = e.do(http.MethodGet, "/login", nil, false)
	i := strings.Index(r.body, "/static/app.js?v=")
	versioned := r.body[i : i+len("/static/app.js?v=")+12]
	r = e.do(http.MethodGet, versioned, nil, false)
	if !strings.Contains(r.header.Get("Cache-Control"), "immutable") {
		t.Errorf("versioned asset: %q", r.header.Get("Cache-Control"))
	}
	if r := e.do(http.MethodGet, "/static/", nil, false); r.status != http.StatusNotFound {
		t.Errorf("directory listing: %d", r.status)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestPWA(t *testing.T) {
	e := newEnv(t) // not logged in: the browser fetches these without a session
	r := e.do(http.MethodGet, "/manifest.webmanifest", nil, false)
	if r.status != 200 || r.header.Get("Content-Type") != "application/manifest+json" {
		t.Fatalf("manifest: %d %s", r.status, r.header.Get("Content-Type"))
	}
	var m struct {
		StartURL string `json:"start_url"`
		Display  string
		Icons    []struct{ Src, Purpose string }
	}
	if err := json.Unmarshal([]byte(r.body), &m); err != nil {
		t.Fatal(err)
	}
	if m.StartURL != "/day" || m.Display != "standalone" || len(m.Icons) != 3 {
		t.Errorf("manifest: %+v", m)
	}
	for _, icon := range m.Icons {
		if r := e.do(http.MethodGet, icon.Src, nil, false); r.status != 200 {
			t.Errorf("icon %s: %d", icon.Src, r.status)
		}
	}

	r = e.do(http.MethodGet, "/sw.js", nil, false)
	if r.status != 200 || r.header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("sw: %d %q", r.status, r.header.Get("Cache-Control"))
	}
	contains(t, "sw.js", r.body, `"/static/app.js?v=`, `"/static/htmx.min.js?v=`, `"/static/icons/maskable-512.png?v=`)

	r = e.do(http.MethodGet, "/login", nil, false)
	contains(t, "layout", r.body, `rel="manifest"`, `rel="apple-touch-icon"`, `name="theme-color"`)
}
