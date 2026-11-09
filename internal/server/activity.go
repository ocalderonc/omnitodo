package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

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

	Title, Kind, Duration, StartAt, DueDate, Tags, Recurrence, Description string
	ProjectID                                                              int64
	Projects                                                               []store.Project
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
	if res.Project != "" {
		p, err := s.store.ResolveProject(r.Context(), res.Project)
		switch {
		case errors.Is(err, store.ErrNotFound):
			s.renderDay(w, r, day, "", fmt.Sprintf("There is no project called “%s”.", res.Project), q)
			return
		case errors.Is(err, store.ErrAmbiguous):
			s.renderDay(w, r, day, "", fmt.Sprintf("More than one company has a project called “%s”: write +Company/%s.", res.Project, res.Project), q)
			return
		case err != nil:
			s.serverError(w, r, err)
			return
		}
		in.ProjectID = &p.ID
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
	projects, err := s.store.ListProjects(r.Context(), false)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderFragment(w, r, "form", formView{Day: day, Kind: string(schedule.KindTask), Duration: "30m", Projects: projects})
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
	projects, err := s.store.ListProjects(r.Context(), false)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := formView{
		ID: a.ID, Day: day, IsTemplate: a.IsTemplate, Projects: projects,
		Title: a.Title, Kind: string(a.Kind), Duration: compactDuration(a.DurationMin),
		Recurrence: a.Recurrence, Description: a.Description,
	}
	if a.StartAt != nil {
		v.StartAt = a.StartAt.String()
	}
	if a.DueDate != nil {
		v.DueDate = a.DueDate.String()
	}
	if a.ProjectID != nil {
		v.ProjectID = *a.ProjectID
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
	}
	in, err := s.formInput(v, r.FormValue("project_id"))
	if err == nil {
		var a store.Activity
		if id == 0 {
			in.PlannedDate = &day
			a, err = s.store.CreateActivity(r.Context(), in)
		} else {
			a, err = s.store.UpdateActivity(r.Context(), id, in)
		}
		if err == nil {
			s.renderDay(w, r, day, fmt.Sprintf("Saved “%s”.", a.Title), "", "")
			return
		}
	}
	if !errors.Is(err, store.ErrInvalid) && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, r, err)
		return
	}

	v.Error = strings.TrimPrefix(err.Error(), store.ErrInvalid.Error()+": ")
	v.ProjectID, _ = strconv.ParseInt(r.FormValue("project_id"), 10, 64)
	if id != 0 {
		if a, err := s.store.GetActivity(r.Context(), id); err == nil {
			v.IsTemplate = a.IsTemplate
		}
	}
	if v.Projects, err = s.store.ListProjects(r.Context(), false); err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("HX-Retarget", "#modal-body")
	w.Header().Set("HX-Reswap", "innerHTML")
	s.renderFragment(w, r, "form", v)
}

func (s *Server) formInput(v formView, projectID string) (store.ActivityInput, error) {
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
	if projectID != "" {
		pid, err := strconv.ParseInt(projectID, 10, 64)
		if err != nil {
			return in, fmt.Errorf("%w: project", store.ErrInvalid)
		}
		in.ProjectID = &pid
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
