package server

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/ocalderonc/omnitodo/internal/store"
)

func TestSettingsRequiresAuth(t *testing.T) {
	e := newEnv(t)
	if r := e.do(http.MethodGet, "/settings", nil, false); r.status != http.StatusSeeOther {
		t.Errorf("GET /settings without session: %d", r.status)
	}
	if r := e.do(http.MethodPost, "/settings/companies", url.Values{"name": {"Acme"}}, false); r.status != http.StatusUnauthorized {
		t.Errorf("POST without session: %d", r.status)
	}
	if cs, _ := e.store.ListCompanies(ctx, true); len(cs) != 0 {
		t.Error("an unauthenticated post created a company")
	}
}

func TestSettingsCompaniesAndProjects(t *testing.T) {
	e := newEnv(t)
	e.login()

	r := e.do(http.MethodGet, "/settings", nil, false)
	if r.status != 200 || !strings.Contains(r.body, "No companies yet") || !strings.Contains(r.body, `href="/settings"`) {
		t.Fatalf("settings page: %d\n%s", r.status, r.body)
	}

	// Plain form post: redirect back.
	r = e.do(http.MethodPost, "/settings/companies", url.Values{"name": {" Acme "}, "color": {"#AA0000"}}, false)
	if r.status != http.StatusSeeOther || r.header.Get("Location") != "/settings" {
		t.Fatalf("create company: %d %s", r.status, r.header.Get("Location"))
	}
	cs, _ := e.store.ListCompanies(ctx, true)
	if len(cs) != 1 || cs[0].Name != "Acme" || cs[0].Color != "#aa0000" {
		t.Fatalf("companies: %+v", cs)
	}
	acme := strconv.FormatInt(cs[0].ID, 10)

	// HTMX: the #settings fragment comes back, with the notice.
	r = e.do(http.MethodPost, "/settings/projects", url.Values{"name": {"Website"}, "company_id": {acme}}, true)
	if r.status != 200 || !strings.HasPrefix(strings.TrimSpace(r.body), `<section id="settings"`) || !strings.Contains(r.body, "Added project “Website”") {
		t.Fatalf("create project: %d\n%s", r.status, r.body)
	}
	ps, _ := e.store.ListProjects(ctx, true)
	if len(ps) != 1 || ps[0].CompanyName != "Acme" || ps[0].Color != "" {
		t.Fatalf("projects: %+v", ps)
	}
	web := strconv.FormatInt(ps[0].ID, 10)

	// Duplicate name in the same company: a message, not a 500.
	r = e.do(http.MethodPost, "/settings/projects", url.Values{"name": {"website"}, "company_id": {acme}}, true)
	if r.status != 200 || !strings.Contains(r.body, "already taken") {
		t.Errorf("duplicate project: %d\n%s", r.status, r.body)
	}
	// Same, as a plain post: the page with a 400.
	r = e.do(http.MethodPost, "/settings/companies", url.Values{"name": {""}}, false)
	if r.status != http.StatusBadRequest || !strings.Contains(r.body, "Name is required") {
		t.Errorf("empty company name: %d\n%s", r.status, r.body)
	}

	// Edit: own color, no company, archived.
	r = e.do(http.MethodPost, "/settings/projects/"+web, url.Values{
		"name": {"Site"}, "company_id": {""}, "own_color": {"1"}, "color": {"#00aa00"}, "archived": {"1"},
	}, true)
	if !strings.Contains(r.body, "Saved project “Site”") {
		t.Fatalf("update project:\n%s", r.body)
	}
	p, _ := e.store.ListProjects(ctx, true)
	if p[0].Name != "Site" || p[0].CompanyID != nil || p[0].Color != "#00aa00" || !p[0].Archived {
		t.Errorf("updated project: %+v", p[0])
	}

	// Deleting the company keeps the project.
	e.do(http.MethodPost, "/settings/projects/"+web, url.Values{"name": {"Site"}, "company_id": {acme}}, true)
	r = e.do(http.MethodPost, "/settings/companies/"+acme+"/delete", nil, true)
	if !strings.Contains(r.body, "Deleted the company") {
		t.Fatalf("delete company:\n%s", r.body)
	}
	if p, _ := e.store.ListProjects(ctx, true); len(p) != 1 || p[0].CompanyID != nil {
		t.Errorf("project after company delete: %+v", p)
	}

	r = e.do(http.MethodPost, "/settings/projects/"+web+"/delete", nil, true)
	if p, _ := e.store.ListProjects(ctx, true); len(p) != 0 || !strings.Contains(r.body, "Deleted the project") {
		t.Errorf("delete project: %+v\n%s", p, r.body)
	}
	if r := e.do(http.MethodPost, "/settings/projects/"+web+"/delete", nil, true); !strings.Contains(r.body, "no longer exists") {
		t.Errorf("delete twice:\n%s", r.body)
	}
}

// Deleting a company whose project clashes with a company-less one must say
// so instead of failing with a 500.
func TestDeleteCompanyNameClash(t *testing.T) {
	e := newEnv(t)
	e.login()
	c, _ := e.store.CreateCompany(ctx, store.Company{Name: "Acme"})
	e.store.CreateProject(ctx, store.Project{Name: "Core", CompanyID: &c.ID})
	e.store.CreateProject(ctx, store.Project{Name: "Core"})
	r := e.do(http.MethodPost, "/settings/companies/"+strconv.FormatInt(c.ID, 10)+"/delete", nil, true)
	if r.status != 200 || !strings.Contains(r.body, "same name as a project without a company") {
		t.Errorf("clash: %d\n%s", r.status, r.body)
	}
}

func TestSettingsTagsAndDay(t *testing.T) {
	e := newEnv(t)
	e.login()
	e.do(http.MethodPost, "/activities", url.Values{"q": {"Read 20m #study"}, "day": {today.String()}}, true)
	tags, _ := e.store.ListTags(ctx)
	if len(tags) != 1 {
		t.Fatalf("tags: %+v", tags)
	}
	id := strconv.FormatInt(tags[0].ID, 10)
	r := e.do(http.MethodPost, "/settings/tags/"+id, url.Values{"name": {"#learning"}, "color": {"#123456"}}, true)
	if tags, _ = e.store.ListTags(ctx); tags[0].Name != "learning" || tags[0].Color != "#123456" {
		t.Errorf("renamed tag: %+v\n%s", tags, r.body)
	}
	e.do(http.MethodPost, "/settings/tags/"+id+"/delete", nil, true)
	if tags, _ = e.store.ListTags(ctx); len(tags) != 0 {
		t.Errorf("tag not deleted: %+v", tags)
	}

	r = e.do(http.MethodPost, "/settings/day", url.Values{"day_start": {"07:30"}, "day_end": {"17:00"}, "timezone": {"Europe/Madrid"}}, true)
	st, _ := e.store.GetSettings(ctx)
	if st.DayStart.String() != "07:30" || st.DayEnd.String() != "17:00" || st.Timezone != "Europe/Madrid" {
		t.Errorf("settings: %+v\n%s", st, r.body)
	}
	r = e.do(http.MethodPost, "/settings/day", url.Values{"day_start": {"18:00"}, "day_end": {"08:00"}, "timezone": {"Europe/Madrid"}}, true)
	if !strings.Contains(r.body, "must start before it ends") {
		t.Errorf("start after end:\n%s", r.body)
	}
	r = e.do(http.MethodPost, "/settings/day", url.Values{"day_start": {"08:00"}, "day_end": {"18:00"}, "timezone": {"Mars/Olympus"}}, true)
	if !strings.Contains(r.body, "Unknown timezone") {
		t.Errorf("bad timezone:\n%s", r.body)
	}
}

func TestQuickAddCreatesMissingProject(t *testing.T) {
	e := newEnv(t)
	e.login()
	day := today.String()

	// Unknown project: an error plus the offer, nothing created.
	r := e.do(http.MethodPost, "/activities", url.Values{"q": {"Fix nav 45m +Website"}, "day": {day}}, true)
	if !strings.Contains(r.body, "There is no project called “Website”") ||
		!strings.Contains(r.body, `name="create_project"`) || !strings.Contains(r.body, "Create project “Website” and add") {
		t.Fatalf("offer missing:\n%s", r.body)
	}
	if acts, _ := e.store.ListDay(ctx, today); len(acts) != 0 {
		t.Fatal("activity created before the project existed")
	}

	// Accepting it creates the project and the activity in one go.
	r = e.do(http.MethodPost, "/activities", url.Values{"q": {"Fix nav 45m +Website"}, "create_project": {"1"}, "day": {day}}, true)
	if !strings.Contains(r.body, "Created project “Website” and added “Fix nav”") {
		t.Fatalf("create:\n%s", r.body)
	}
	acts, _ := e.store.ListDay(ctx, today)
	if len(acts) != 1 || acts[0].ProjectName != "Website" {
		t.Fatalf("activities: %+v", acts)
	}

	// Company/Project attaches to an existing company...
	e.store.CreateCompany(ctx, store.Company{Name: "Acme"})
	r = e.do(http.MethodPost, "/activities", url.Values{"q": {"Plan 30m +acme/Launch"}, "create_project": {"1"}, "day": {day}}, true)
	if !strings.Contains(r.body, "Created project “Acme / Launch”") {
		t.Fatalf("qualified create:\n%s", r.body)
	}
	// ...but never invents a company from a typo.
	r = e.do(http.MethodPost, "/activities", url.Values{"q": {"Plan 30m +Acne/Launch"}, "create_project": {"1"}, "day": {day}}, true)
	if !strings.Contains(r.body, "There is no company called “Acne”") {
		t.Errorf("unknown company:\n%s", r.body)
	}
	if cs, _ := e.store.ListCompanies(ctx, true); len(cs) != 1 {
		t.Errorf("companies: %+v", cs)
	}

	// An archived project with that name: say so rather than "already taken".
	ps, _ := e.store.ListProjects(ctx, false)
	for _, p := range ps {
		if p.Name == "Website" {
			p.Archived = true
			e.store.UpdateProject(ctx, p)
		}
	}
	r = e.do(http.MethodPost, "/activities", url.Values{"q": {"More 10m +Website"}, "create_project": {"1"}, "day": {day}}, true)
	if !strings.Contains(r.body, "already exists but is archived") {
		t.Errorf("archived:\n%s", r.body)
	}
}
