package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/ocalderonc/omnitodo/internal/civil"
	"github.com/ocalderonc/omnitodo/internal/store"
)

// The settings page: companies, projects, tags and the working day. Every
// form posts and gets the whole #settings section back (HTMX), or a redirect
// to /settings (plain post), the same way the day page works.

type settingsView struct {
	Auth      bool
	Companies []store.Company
	Projects  []projectRow
	Tags      []store.Tag

	DayStart, DayEnd, Timezone string

	Notice, Error string
}

type projectRow struct {
	store.Project
	CompanyID int64  // 0 = no company
	OwnColor  bool   // false = uses the company's color
	Swatch    string // the color the picker shows
}

func (s *Server) buildSettings(r *http.Request) (settingsView, error) {
	ctx := r.Context()
	v := settingsView{Auth: !s.cfg.NoAuth}
	var err error
	if v.Companies, err = s.store.ListCompanies(ctx, true); err != nil {
		return v, err
	}
	projects, err := s.store.ListProjects(ctx, true)
	if err != nil {
		return v, err
	}
	colors := map[int64]string{}
	for _, c := range v.Companies {
		colors[c.ID] = c.Color
	}
	for _, p := range projects {
		row := projectRow{Project: p, OwnColor: p.Color != "", Swatch: p.Color}
		if p.CompanyID != nil {
			row.CompanyID = *p.CompanyID
		}
		if row.Swatch == "" {
			row.Swatch = colors[row.CompanyID]
		}
		if row.Swatch == "" {
			row.Swatch = "#6b7280"
		}
		v.Projects = append(v.Projects, row)
	}
	if v.Tags, err = s.store.ListTags(ctx); err != nil {
		return v, err
	}
	st, err := s.store.GetSettings(ctx)
	if err != nil {
		return v, err
	}
	v.DayStart, v.DayEnd, v.Timezone = st.DayStart.String(), st.DayEnd.String(), st.Timezone
	return v, nil
}

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	v, err := s.buildSettings(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderPage(w, r, http.StatusOK, "settings", "Settings", v)
}

// settingsDone answers a settings form. User errors (invalid input, a name
// already taken, an unknown row) are shown above the forms; anything else is
// a 500.
func (s *Server) settingsDone(w http.ResponseWriter, r *http.Request, notice string, err error) {
	msg := ""
	if err != nil {
		if !errors.Is(err, store.ErrInvalid) && !errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrNotFound) {
			s.serverError(w, r, err)
			return
		}
		msg = userMessage(err)
	}
	if err == nil && !isHTMX(r) {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	v, verr := s.buildSettings(r)
	if verr != nil {
		s.serverError(w, r, verr)
		return
	}
	v.Error = msg
	if err == nil {
		v.Notice = notice
	}
	if isHTMX(r) {
		// 200 even on error: HTMX does not swap 4xx responses by default.
		s.render(w, r, http.StatusOK, s.pages["settings"], "settings", v)
		return
	}
	s.renderPage(w, r, http.StatusBadRequest, "settings", "Settings", v)
}

// userMessage turns a store error into a sentence for the page.
func userMessage(err error) string {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return "That item no longer exists; the page has been refreshed."
	case errors.Is(err, store.ErrConflict):
		msg := strings.TrimSuffix(err.Error(), ": "+store.ErrConflict.Error())
		if msg == err.Error() {
			return "That name is already taken."
		}
		return capitalize(msg) + ": that name is already taken."
	}
	return capitalize(strings.TrimPrefix(err.Error(), store.ErrInvalid.Error()+": "))
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func formID(r *http.Request, field string) (*int64, error) {
	raw := r.FormValue(field)
	if raw == "" || raw == "0" {
		return nil, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("%w: bad %s", store.ErrInvalid, field)
	}
	return &id, nil
}

func checked(r *http.Request, field string) bool { return r.FormValue(field) != "" }

// --- companies ----------------------------------------------------------------

func (s *Server) createCompany(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.CreateCompany(r.Context(), store.Company{Name: r.FormValue("name"), Color: r.FormValue("color")})
	s.settingsDone(w, r, fmt.Sprintf("Added company “%s”.", c.Name), err)
}

func (s *Server) updateCompany(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	c := store.Company{ID: id, Name: r.FormValue("name"), Color: r.FormValue("color"), Archived: checked(r, "archived")}
	err = s.store.UpdateCompany(r.Context(), c)
	s.settingsDone(w, r, fmt.Sprintf("Saved company “%s”.", strings.TrimSpace(c.Name)), err)
}

func (s *Server) deleteCompany(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.settingsDone(w, r, "Deleted the company. Its projects are kept, without a company.", s.store.DeleteCompany(r.Context(), id))
}

// --- projects -----------------------------------------------------------------

func (s *Server) projectForm(r *http.Request, id int64) (store.Project, error) {
	companyID, err := formID(r, "company_id")
	p := store.Project{ID: id, CompanyID: companyID, Name: r.FormValue("name"), Archived: checked(r, "archived")}
	if checked(r, "own_color") {
		p.Color = r.FormValue("color")
	}
	return p, err
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.projectForm(r, 0)
	if err == nil {
		p, err = s.store.CreateProject(r.Context(), p)
	}
	s.settingsDone(w, r, fmt.Sprintf("Added project “%s”.", p.Name), err)
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p, err := s.projectForm(r, id)
	if err == nil {
		err = s.store.UpdateProject(r.Context(), p)
	}
	s.settingsDone(w, r, fmt.Sprintf("Saved project “%s”.", strings.TrimSpace(p.Name)), err)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.settingsDone(w, r, "Deleted the project. Its activities are kept, without a project.", s.store.DeleteProject(r.Context(), id))
}

// --- tags -----------------------------------------------------------------------

func (s *Server) updateTag(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	t := store.Tag{ID: id, Name: strings.TrimPrefix(strings.TrimSpace(r.FormValue("name")), "#"), Color: r.FormValue("color")}
	err = s.store.UpdateTag(r.Context(), t)
	s.settingsDone(w, r, fmt.Sprintf("Saved tag #%s.", t.Name), err)
}

func (s *Server) deleteTag(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.settingsDone(w, r, "Deleted the tag and removed it from its activities.", s.store.DeleteTag(r.Context(), id))
}

// --- the working day --------------------------------------------------------------

func (s *Server) updateDaySettings(w http.ResponseWriter, r *http.Request) {
	start, err1 := civil.ParseTime(r.FormValue("day_start"))
	end, err2 := civil.ParseTime(r.FormValue("day_end"))
	var err error
	if err1 != nil || err2 != nil {
		err = fmt.Errorf("%w: day start and end must be times like 08:00", store.ErrInvalid)
	} else {
		err = s.store.UpdateSettings(r.Context(), store.Settings{
			DayStart: start, DayEnd: end, Timezone: strings.TrimSpace(r.FormValue("timezone")),
		})
	}
	s.settingsDone(w, r, "Saved the working day.", err)
}

// --- quick add: create the missing project ------------------------------------------

// createProjectRef creates the project a quick-add "+ref" named, where ref is
// "Project" or "Company/Project". The company must already exist: a typo in
// it should not silently create a second company.
func (s *Server) createProjectRef(r *http.Request, ref string) (store.Project, error) {
	company, name, qualified := strings.Cut(strings.TrimSpace(ref), "/")
	p := store.Project{Name: company}
	if qualified {
		c, err := s.store.CompanyByName(r.Context(), company)
		if errors.Is(err, store.ErrNotFound) {
			return store.Project{}, fmt.Errorf("%w: there is no company called “%s”; add it in Settings first", store.ErrInvalid, company)
		} else if err != nil {
			return store.Project{}, err
		}
		p = store.Project{Name: name, CompanyID: &c.ID, CompanyName: c.Name}
	}
	created, err := s.store.CreateProject(r.Context(), p)
	created.CompanyName = p.CompanyName
	return created, err
}
