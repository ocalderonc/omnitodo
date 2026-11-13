package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ocalderonc/omnitodo/internal/civil"
	"github.com/ocalderonc/omnitodo/internal/quickadd"
	"github.com/ocalderonc/omnitodo/internal/schedule"
	"github.com/ocalderonc/omnitodo/internal/store"
)

type formView struct {
	ID         int64
	Day        civil.Date
	IsTemplate bool
	Error      string
	// Date is the day the activity is planned for, shown in the form and
	// pre-set to the day being viewed. FixedDay hides it: recurring
	// activities keep their day.
	Date     string
	FixedDay bool

	Title, Kind, Duration, StartAt, DueDate, Tags, Recurrence, Description string
	Owner                                                                  string // "p:<id>", "c:<id>" or ""
	Owners                                                                 []ownerGroup

	// Voice drafts: what was heard, and what the owner should check.
	Voice bool
	Heard string
	Notes []string
}

// The form's one "Project" select also offers each company on its own:
// grouped by company, its first option is the company with no project.
type ownerGroup struct {
	Label   string
	Options []ownerOption
}

type ownerOption struct{ Value, Label string }

func ownerOf(a store.Activity) string {
	switch {
	case a.ProjectID != nil:
		return "p:" + strconv.FormatInt(*a.ProjectID, 10)
	case a.CompanyID != nil:
		return "c:" + strconv.FormatInt(*a.CompanyID, 10)
	}
	return ""
}

// ownerGroups lists active companies and projects. keep is the activity's
// current owner, listed even if archived so saving does not drop it.
func (s *Server) ownerGroups(r *http.Request, keep string, keepLabel string) ([]ownerGroup, error) {
	companies, err := s.store.ListCompanies(r.Context(), false)
	if err != nil {
		return nil, err
	}
	projects, err := s.store.ListProjects(r.Context(), false)
	if err != nil {
		return nil, err
	}
	var groups []ownerGroup
	index := map[int64]int{}
	for _, c := range companies {
		id := strconv.FormatInt(c.ID, 10)
		index[c.ID] = len(groups)
		groups = append(groups, ownerGroup{Label: c.Name, Options: []ownerOption{{"c:" + id, c.Name + " (no project)"}}})
	}
	var loose []ownerOption
	for _, p := range projects {
		opt := ownerOption{"p:" + strconv.FormatInt(p.ID, 10), p.Name}
		if i, ok := index[derefOr(p.CompanyID, -1)]; ok {
			groups[i].Options = append(groups[i].Options, opt)
		} else {
			loose = append(loose, opt)
		}
	}
	if len(loose) > 0 {
		groups = append(groups, ownerGroup{Label: "No company", Options: loose})
	}
	if keep != "" {
		for _, g := range groups {
			for _, o := range g.Options {
				if o.Value == keep {
					return groups, nil
				}
			}
		}
		groups = append(groups, ownerGroup{Label: "Archived", Options: []ownerOption{{keep, keepLabel}}})
	}
	return groups, nil
}

func derefOr(p *int64, def int64) int64 {
	if p == nil {
		return def
	}
	return *p
}

func ownerLabel(a store.Activity) string {
	switch {
	case a.ProjectName != "" && a.CompanyName != "":
		return a.CompanyName + " / " + a.ProjectName
	case a.ProjectName != "":
		return a.ProjectName
	}
	return a.CompanyName + " (no project)"
}

// formDay is the day the request was made from (the "day" value HTMX sends
// along from #day, or the form's hidden field), defaulting to today.
func (s *Server) formDay(r *http.Request) (civil.Date, error) {
	if raw := r.FormValue("day"); raw != "" {
		return civil.ParseDate(raw)
	}
	d, _, err := s.today(r.Context())
	return d, err
}

func idParam(r *http.Request) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
}

// --- quick add and form create -------------------------------------------------

func (s *Server) createActivity(w http.ResponseWriter, r *http.Request) {
	day, err := s.formDay(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.FormValue("form") != "" {
		s.saveForm(w, r, 0, day)
		return
	}

	q := strings.TrimSpace(r.FormValue("q"))
	if q == "" {
		s.renderDay(w, r, day, "", "", "")
		return
	}
	today, _, err := s.today(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// Relative due dates (!tomorrow, !fri) count from the real today, not from
	// the day being viewed: "due tomorrow" means tomorrow.
	res, err := quickadd.Parse(q, today)
	if err != nil {
		s.renderDay(w, r, day, "", err.Error(), q)
		return
	}
	in := store.ActivityInput{
		Kind: schedule.KindTask, Title: res.Title, DurationMin: res.DurationMin, PlannedDate: &day,
		StartAt: res.StartAt, DueDate: res.DueDate, Recurrence: res.Recurrence, Tags: res.Tags,
	}
	if res.Distractor {
		in.Kind = schedule.KindDistractor
	}
	created := ""
	companyOnly, wantCompany := strings.CutSuffix(res.Project, "/")
	if res.Project != "" && wantCompany {
		// "+Acme/": the company itself, no project.
		c, err := s.store.CompanyByName(r.Context(), companyOnly)
		switch {
		case errors.Is(err, store.ErrNotFound):
			s.renderDay(w, r, day, "", fmt.Sprintf("There is no company called “%s”: add it in Settings.", companyOnly), q)
			return
		case err != nil:
			s.serverError(w, r, err)
			return
		case c.Archived:
			s.renderDay(w, r, day, "", fmt.Sprintf("The company “%s” is archived: unarchive it in Settings.", c.Name), q)
			return
		}
		in.CompanyID = &c.ID
	} else if res.Project != "" {
		p, err := s.store.ResolveProject(r.Context(), res.Project)
		if errors.Is(err, store.ErrNotFound) && !strings.Contains(res.Project, "/") {
			// "+Acme" with no project of that name: the company Acme, if any.
			if c, cerr := s.store.CompanyByName(r.Context(), res.Project); cerr == nil && !c.Archived {
				in.CompanyID, err = &c.ID, nil
			} else if cerr != nil && !errors.Is(cerr, store.ErrNotFound) {
				s.serverError(w, r, cerr)
				return
			}
		}
		if errors.Is(err, store.ErrNotFound) && r.FormValue("create_project") != "" {
			p, err = s.createProjectRef(r, res.Project)
			switch {
			case errors.Is(err, store.ErrConflict):
				s.renderDay(w, r, day, "", fmt.Sprintf("A project called “%s” already exists but is archived: unarchive it in Settings.", res.Project), q)
				return
			case errors.Is(err, store.ErrInvalid):
				s.renderDay(w, r, day, "", userMessage(err)+".", q)
				return
			case err == nil:
				created = p.Name
				if p.CompanyName != "" {
					created = p.CompanyName + " / " + p.Name
				}
			}
		}
		switch {
		case errors.Is(err, store.ErrNotFound):
			s.renderDayView(w, r, day, func(v *dayView) {
				v.Error, v.QuickAdd, v.MissingProject = missingMessage(res.Project), q, res.Project
			})
			return
		case errors.Is(err, store.ErrAmbiguous):
			s.renderDay(w, r, day, "", fmt.Sprintf("More than one company has a project called “%s”: write +Company/%s.", res.Project, res.Project), q)
			return
		case err != nil:
			s.serverError(w, r, err)
			return
		}
		if in.CompanyID == nil {
			in.ProjectID = &p.ID
		}
	}
	a, err := s.store.CreateActivity(r.Context(), in)
	if errors.Is(err, store.ErrInvalid) {
		s.renderDay(w, r, day, "", err.Error(), q)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	notice := fmt.Sprintf("Added “%s”", a.Title)
	if created != "" {
		notice = fmt.Sprintf("Created project “%s” and added “%s”", created, a.Title)
	}
	if res.DefaultedDuration {
		notice += fmt.Sprintf(" with the default %s (add e.g. 45m to set it)", schedule.FormatDuration(a.DurationMin))
	}
	if a.IsTemplate {
		notice += ", repeating " + a.Recurrence
	}
	s.renderDay(w, r, day, notice+".", "", "")
}

// --- the edit dialog --------------------------------------------------------------

func (s *Server) newForm(w http.ResponseWriter, r *http.Request) {
	day, err := s.formDay(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	owners, err := s.ownerGroups(r, "", "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderFragment(w, r, "form", formView{Day: day, Date: day.String(), Kind: string(schedule.KindTask), Duration: "30m", Owners: owners})
}

func (s *Server) editForm(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	a, err := s.store.GetActivity(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	day, err := s.formDay(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	owners, err := s.ownerGroups(r, ownerOf(a), ownerLabel(a))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := formView{
		ID: a.ID, Day: day, IsTemplate: a.IsTemplate, Owners: owners, Owner: ownerOf(a),
		Title: a.Title, Kind: string(a.Kind), Duration: compactDuration(a.DurationMin),
		Recurrence: a.Recurrence, Description: a.Description,
		FixedDay: a.IsTemplate || a.TemplateID != nil,
	}
	if a.PlannedDate != nil {
		v.Date = a.PlannedDate.String()
	}
	if a.StartAt != nil {
		v.StartAt = a.StartAt.String()
	}
	if a.DueDate != nil {
		v.DueDate = a.DueDate.String()
	}
	var tags []string
	for _, t := range a.Tags {
		tags = append(tags, t.Name)
	}
	v.Tags = strings.Join(tags, ", ")
	s.renderFragment(w, r, "form", v)
}

func compactDuration(min int) string {
	return strings.ReplaceAll(schedule.FormatDuration(min), " ", "")
}

func (s *Server) updateActivity(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	day, err := s.formDay(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.saveForm(w, r, id, day)
}

// saveForm creates (id 0) or updates an activity from the dialog form. On a
// validation error the form comes back into the dialog with the message.
func (s *Server) saveForm(w http.ResponseWriter, r *http.Request, id int64, day civil.Date) {
	v := formView{
		ID: id, Day: day,
		Title: r.FormValue("title"), Kind: r.FormValue("kind"), Duration: r.FormValue("duration"),
		StartAt: r.FormValue("start_at"), DueDate: r.FormValue("due_date"), Tags: r.FormValue("tags"),
		Recurrence: strings.TrimSpace(r.FormValue("recurrence")), Description: r.FormValue("description"),
		Date: strings.TrimSpace(r.FormValue("date")),
	}
	v.Owner = r.FormValue("owner")
	in, err := s.formInput(v)
	planned := day
	if err == nil && v.Date != "" {
		if planned, err = civil.ParseDate(v.Date); err != nil {
			err = fmt.Errorf("%w: date %q", store.ErrInvalid, v.Date)
		}
	}
	if err == nil {
		var a store.Activity
		if id == 0 {
			in.PlannedDate = &planned
			a, err = s.store.CreateActivity(r.Context(), in)
		} else if a, err = s.store.UpdateActivity(r.Context(), id, in); err == nil && v.Date != "" &&
			!a.IsTemplate && a.TemplateID == nil && (a.PlannedDate == nil || *a.PlannedDate != planned) {
			// A changed date moves it, to the end of that day's list.
			err = s.store.MoveToDate(r.Context(), id, &planned)
		}
		if err == nil {
			notice := fmt.Sprintf("Saved “%s”.", a.Title)
			if planned != day && !a.IsTemplate {
				notice = fmt.Sprintf("Saved “%s” on %s.", a.Title, planned.In(time.UTC).Format("Monday 2 January"))
			}
			s.renderDay(w, r, day, notice, "", "")
			return
		}
	}
	if !errors.Is(err, store.ErrInvalid) && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, r, err)
		return
	}

	v.Error = strings.TrimPrefix(err.Error(), store.ErrInvalid.Error()+": ")
	keep, keepLabel := "", ""
	if id != 0 {
		if a, err := s.store.GetActivity(r.Context(), id); err == nil {
			v.IsTemplate = a.IsTemplate
			v.FixedDay = a.IsTemplate || a.TemplateID != nil
			if ownerOf(a) == v.Owner {
				keep, keepLabel = v.Owner, ownerLabel(a)
			}
		}
	}
	if v.Owners, err = s.ownerGroups(r, keep, keepLabel); err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("HX-Retarget", "#modal-body")
	w.Header().Set("HX-Reswap", "innerHTML")
	s.renderFragment(w, r, "form", v)
}

func (s *Server) formInput(v formView) (store.ActivityInput, error) {
	in := store.ActivityInput{
		Kind: schedule.Kind(v.Kind), Title: v.Title, Description: v.Description, Recurrence: v.Recurrence,
	}
	dur := strings.ReplaceAll(strings.TrimSpace(v.Duration), " ", "")
	if n, err := strconv.Atoi(dur); err == nil && n > 0 {
		in.DurationMin = n // a bare number is minutes
	} else if n, ok := quickadd.ParseDuration(dur); ok {
		in.DurationMin = n
	} else {
		return in, fmt.Errorf("%w: duration %q: write it like 45m, 2h or 1h30m", store.ErrInvalid, v.Duration)
	}
	if v.StartAt != "" {
		t, err := civil.ParseTime(v.StartAt[:min(len(v.StartAt), 5)]) // browsers may send HH:MM:SS
		if err != nil {
			return in, fmt.Errorf("%w: %v", store.ErrInvalid, err)
		}
		in.StartAt = &t
	}
	if v.DueDate != "" {
		d, err := civil.ParseDate(v.DueDate)
		if err != nil {
			return in, fmt.Errorf("%w: %v", store.ErrInvalid, err)
		}
		in.DueDate = &d
	}
	if v.Owner != "" {
		kind, raw, _ := strings.Cut(v.Owner, ":")
		id, err := strconv.ParseInt(raw, 10, 64)
		switch {
		case err != nil:
			return in, fmt.Errorf("%w: project", store.ErrInvalid)
		case kind == "p":
			in.ProjectID = &id
		case kind == "c":
			in.CompanyID = &id
		default:
			return in, fmt.Errorf("%w: project", store.ErrInvalid)
		}
	}
	for _, t := range strings.FieldsFunc(v.Tags, func(r rune) bool { return r == ',' || r == ' ' }) {
		if t = strings.TrimPrefix(t, "#"); t != "" {
			in.Tags = append(in.Tags, t)
		}
	}
	return in, nil
}

// --- toggle and delete -------------------------------------------------------------

func (s *Server) toggleActivity(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	day, err := s.formDay(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := s.store.ToggleDone(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderDay(w, r, day, "", "", "")
}

func (s *Server) deleteActivity(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	day, err := s.formDay(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a, err := s.store.GetActivity(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.store.DeleteActivity(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	notice := fmt.Sprintf("Deleted “%s”.", a.Title)
	if a.TemplateID != nil {
		notice = fmt.Sprintf("Skipped “%s” for this day.", a.Title)
	}
	s.renderDay(w, r, day, notice, "", "")
}

func missingMessage(ref string) string {
	if strings.Contains(ref, "/") {
		return fmt.Sprintf("There is no project called “%s”.", ref)
	}
	return fmt.Sprintf("There is no project or company called “%s”.", ref)
}
