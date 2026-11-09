package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/ocalderonc/omnitodo/internal/store"
)

// Tasks can belong to a company without a project.
func TestQuickAddCompanyWithoutProject(t *testing.T) {
	e := newEnv(t)
	e.login()
	day := today.String()
	acme, _ := e.store.CreateCompany(ctx, store.Company{Name: "Acme", Color: "#aa0000"})
	add := func(q string) resp {
		return e.do(http.MethodPost, "/activities", url.Values{"q": {q}, "day": {day}}, true)
	}

	// "+Acme": no project of that name, so the company.
	r := add("Invoice 30m +acme")
	if !strings.Contains(r.body, "Added “Invoice”") || !strings.Contains(r.body, `<span class="proj company">Acme</span>`) {
		t.Fatalf("company quick add:\n%s", r.body)
	}
	acts, _ := e.store.ListDay(ctx, today)
	if len(acts) != 1 || acts[0].ProjectID != nil || acts[0].CompanyID == nil || *acts[0].CompanyID != acme.ID {
		t.Fatalf("activity: %+v", acts)
	}

	// A project with the company's name wins; "+Acme/" still means the company.
	e.store.CreateProject(ctx, store.Project{Name: "Acme"})
	add("Ship 15m +Acme")
	add("Call 10m +ACME/")
	acts, _ = e.store.ListDay(ctx, today)
	if acts[1].ProjectName != "Acme" || acts[1].CompanyID != nil {
		t.Errorf("+Acme with a project called Acme: %+v", acts[1])
	}
	if acts[2].ProjectID != nil || acts[2].CompanyName != "Acme" {
		t.Errorf("+ACME/: %+v", acts[2])
	}

	// Unknown and archived companies.
	if r := add("X 10m +Nope/"); !strings.Contains(r.body, "There is no company called “Nope”") {
		t.Errorf("unknown company:\n%s", r.body)
	}
	e.store.UpdateCompany(ctx, store.Company{ID: acme.ID, Name: "Acme", Color: "#aa0000", Archived: true})
	if r := add("X 10m +Acme/"); !strings.Contains(r.body, "is archived") {
		t.Errorf("archived company:\n%s", r.body)
	}
	e.store.UpdateCompany(ctx, store.Company{ID: acme.ID, Name: "Acme", Color: "#aa0000"})

	// The company's totals include its project-less tasks (30m + 10m).
	r = e.do(http.MethodGet, "/api/day/"+day, nil, false)
	var j struct {
		Summary struct {
			ByCompany map[string]struct{ Task int } `json:"by_company"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(r.body), &j); err != nil {
		t.Fatal(err)
	}
	if j.Summary.ByCompany["Acme"].Task != 40 {
		t.Errorf("by_company: %+v", j.Summary.ByCompany)
	}
}

func TestFormCompanyOwner(t *testing.T) {
	e := newEnv(t)
	e.login()
	day := today.String()
	acme, _ := e.store.CreateCompany(ctx, store.Company{Name: "Acme"})
	web, _ := e.store.CreateProject(ctx, store.Project{Name: "Web", CompanyID: &acme.ID})
	cid, pid := "c:"+strconv.FormatInt(acme.ID, 10), "p:"+strconv.FormatInt(web.ID, 10)

	r := e.do(http.MethodGet, "/activities/new", url.Values{"day": {day}}, true)
	if !strings.Contains(r.body, `<optgroup label="Acme">`) || !strings.Contains(r.body, `value="`+cid+`">Acme (no project)`) ||
		!strings.Contains(r.body, `value="`+pid+`">Web`) {
		t.Fatalf("form options:\n%s", r.body)
	}

	form := url.Values{"form": {"1"}, "day": {day}, "title": {"Budget"}, "kind": {"task"}, "duration": {"1h"}, "owner": {cid}}
	e.do(http.MethodPost, "/activities", form, true)
	acts, _ := e.store.ListDay(ctx, today)
	if len(acts) != 1 || acts[0].CompanyID == nil || acts[0].ProjectID != nil {
		t.Fatalf("created: %+v", acts)
	}
	id := strconv.FormatInt(acts[0].ID, 10)

	r = e.do(http.MethodGet, "/activities/"+id+"/edit", url.Values{"day": {day}}, true)
	if !strings.Contains(r.body, `value="`+cid+`" selected`) {
		t.Errorf("edit form does not preselect the company:\n%s", r.body)
	}

	// Switching to a project clears the direct company.
	form.Set("owner", pid)
	e.do(http.MethodPatch, "/activities/"+id, form, true)
	a, _ := e.store.GetActivity(ctx, acts[0].ID)
	if a.ProjectID == nil || a.CompanyID != nil || a.CompanyName != "Acme" {
		t.Errorf("after switching to a project: %+v", a)
	}

	// Deleting the project keeps the activity with the company.
	e.store.DeleteProject(ctx, web.ID)
	a, _ = e.store.GetActivity(ctx, acts[0].ID)
	if a.ProjectID != nil || a.CompanyID == nil || a.CompanyName != "Acme" {
		t.Errorf("after deleting the project: %+v", a)
	}

	// An archived owner stays selectable in the edit form, so saving keeps it.
	e.store.UpdateCompany(ctx, store.Company{ID: acme.ID, Name: "Acme", Archived: true})
	r = e.do(http.MethodGet, "/activities/"+id+"/edit", url.Values{"day": {day}}, true)
	if !strings.Contains(r.body, `<optgroup label="Archived">`) || !strings.Contains(r.body, `value="`+cid+`" selected`) {
		t.Errorf("archived owner missing from the edit form:\n%s", r.body)
	}

	if r := e.do(http.MethodPost, "/activities", url.Values{"form": {"1"}, "day": {day}, "title": {"X"}, "duration": {"5m"}, "owner": {"z:1"}}, true); !strings.Contains(r.body, "project") || !strings.Contains(r.body, `class="error"`) {
		t.Errorf("bad owner:\n%s", r.body)
	}
}
