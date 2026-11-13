package server

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ocalderonc/omnitodo/internal/civil"
	"github.com/ocalderonc/omnitodo/internal/schedule"
	"github.com/ocalderonc/omnitodo/internal/store"
)

type dayView struct {
	Date, Prev, Next civil.Date
	Label            string
	IsToday          bool
	Auth             bool

	Summary   schedule.Summary
	Companies []companyChip
	Items     []itemView
	Timeline  timelineView

	QuickAdd       string // echoed back after a parse error
	Error          string
	Notice         string
	MissingProject string // a quick-add "+ref" that matched nothing: offer to create it
	VoiceEnabled   bool
}

type companyChip struct {
	Name       string
	Task       int
	Distractor int
	Style      template.CSS
}

type itemView struct {
	store.Activity
	Range    string // "09:00–09:45" on the timeline, "" if skipped
	Anchored bool
	Done     bool
	Flags    []schedule.Flag
	Style    template.CSS
}

type timelineView struct {
	From   int // the minute of the day at the top, for the drag script
	Style  template.CSS
	Hours  []hourMark
	Blocks []blockView
	Now    template.CSS // "" when the day is not today or now is off-scale
}

type hourMark struct {
	Half  bool // a half-hour line, shown only when zoomed in
	Label string
	Style template.CSS
}

type blockView struct {
	// ID is 0 for free time. Start and Dur are minutes, for the drag script.
	ID         int64
	Start, Dur int
	Anchored   bool
	Kind       schedule.Kind
	Title      string
	Range      string
	Done       bool
	Flags      []schedule.Flag
	Style      template.CSS
}

// colorStyle is safe to mark as CSS because the store only ever accepts
// #rrggbb colors (store.cleanColor) and the rest are integers we format.
func colorStyle(color string) string {
	if color == "" {
		return ""
	}
	return "--c:" + color + ";"
}

func timeRange(a, b civil.Time) string { return a.String() + "–" + b.String() }

// today is the current date in the configured timezone.
func (s *Server) today(ctx context.Context) (civil.Date, store.Settings, error) {
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return civil.Date{}, settings, err
	}
	loc, err := settings.Location()
	if err != nil {
		return civil.Date{}, settings, err
	}
	return civil.DateOf(s.cfg.Now().In(loc)), settings, nil
}

func (s *Server) buildDay(ctx context.Context, d civil.Date) (dayView, error) {
	today, settings, err := s.today(ctx)
	if err != nil {
		return dayView{}, err
	}
	acts, err := s.store.ListDay(ctx, d)
	if err != nil {
		return dayView{}, err
	}
	companies, err := s.store.ListCompanies(ctx, true)
	if err != nil {
		return dayView{}, err
	}

	items := make([]schedule.Activity, len(acts))
	for i, a := range acts {
		items[i] = a.ScheduleItem()
	}
	blocks, sum := schedule.BuildDay(d, items, settings.Schedule())

	v := dayView{
		Date: d, Prev: d.AddDays(-1), Next: d.AddDays(1),
		Label:        d.In(time.UTC).Format("Monday, 2 January 2006"),
		IsToday:      d == today,
		Auth:         !s.cfg.NoAuth,
		Summary:      sum,
		VoiceEnabled: s.cfg.Voice.Transcribes(),
	}

	byID := map[int64]schedule.Block{}
	for _, b := range blocks {
		if b.Kind != schedule.KindFree {
			byID[b.ActivityID] = b
		}
	}
	for _, a := range acts {
		iv := itemView{Activity: a, Done: a.Status == schedule.StatusDone, Style: template.CSS(colorStyle(a.Color))}
		if b, ok := byID[a.ID]; ok {
			iv.Range, iv.Anchored, iv.Flags = timeRange(b.Start, b.End), b.Anchored, b.Flags
		}
		v.Items = append(v.Items, iv)
	}

	colors := map[string]string{}
	for _, c := range companies {
		colors[c.Name] = c.Color
	}
	for name, split := range sum.ByCompany {
		v.Companies = append(v.Companies, companyChip{
			Name: name, Task: split.Task, Distractor: split.Distractor,
			Style: template.CSS(colorStyle(colors[name])),
		})
	}
	slices.SortFunc(v.Companies, func(a, b companyChip) int {
		return cmp.Or(cmp.Compare(b.Task, a.Task), cmp.Compare(a.Name, b.Name))
	})

	var now *civil.Time
	if v.IsToday {
		loc, _ := settings.Location()
		t := s.cfg.Now().In(loc)
		n := civil.NewTime(t.Hour(), t.Minute())
		now = &n
	}
	v.Timeline = buildTimeline(blocks, acts, settings, now)
	return v, nil
}

func buildTimeline(blocks []schedule.Block, acts []store.Activity, settings store.Settings, now *civil.Time) timelineView {
	from, to := settings.DayStart, settings.DayEnd
	for _, b := range blocks {
		from, to = min(from, b.Start), max(to, b.End)
	}
	from = from / 60 * 60    // round down to the hour
	to = (to + 59) / 60 * 60 // and up
	tv := timelineView{From: int(from), Style: template.CSS(fmt.Sprintf("--len:%d;", to-from))}
	for h := from; h <= to; h += 30 {
		m := hourMark{Half: h%60 != 0, Style: template.CSS(fmt.Sprintf("--s:%d;", h-from))}
		if !m.Half {
			m.Label = h.String()
		}
		tv.Hours = append(tv.Hours, m)
	}

	byID := map[int64]store.Activity{}
	for _, a := range acts {
		byID[a.ID] = a
	}
	// Lanes: only conflicting anchors overlap. Each cluster of overlapping
	// blocks gets its own side-by-side columns (greedy first-fit); everything
	// else keeps the full width.
	lanes, laneCount := make([]int, len(blocks)), make([]int, len(blocks))
	for i := 0; i < len(blocks); {
		j, end := i, blocks[i].End
		for j+1 < len(blocks) && blocks[j+1].Start < end {
			j++
			end = max(end, blocks[j].End)
		}
		var laneEnds []civil.Time
		for k := i; k <= j; k++ {
			lane := slices.IndexFunc(laneEnds, func(e civil.Time) bool { return e <= blocks[k].Start })
			if lane < 0 {
				lane = len(laneEnds)
				laneEnds = append(laneEnds, 0)
			}
			laneEnds[lane] = blocks[k].End
			lanes[k] = lane
		}
		for k := i; k <= j; k++ {
			laneCount[k] = len(laneEnds)
		}
		i = j + 1
	}
	for i, b := range blocks {
		bv := blockView{ID: b.ActivityID, Start: int(b.Start), Dur: int(b.End - b.Start), Anchored: b.Anchored,
			Kind: b.Kind, Range: timeRange(b.Start, b.End), Flags: b.Flags}
		color := ""
		if b.Kind == schedule.KindFree {
			bv.Title = "free " + schedule.FormatDuration(int(b.End-b.Start))
		} else {
			a := byID[b.ActivityID]
			bv.Title, bv.Done, color = a.Title, a.Status == schedule.StatusDone, a.Color
		}
		bv.Style = template.CSS(fmt.Sprintf("--s:%d;--d:%d;--lane:%d;--lanes:%d;%s",
			b.Start-from, b.End-b.Start, lanes[i], laneCount[i], colorStyle(color)))
		tv.Blocks = append(tv.Blocks, bv)
	}
	if now != nil && *now >= from && *now <= to {
		tv.Now = template.CSS(fmt.Sprintf("--s:%d;", *now-from))
	}
	return tv
}

// dayParam reads the {date} URL segment, defaulting to today.
func (s *Server) dayParam(r *http.Request, raw string) (civil.Date, error) {
	if raw == "" {
		d, _, err := s.today(r.Context())
		return d, err
	}
	return civil.ParseDate(raw)
}

func (s *Server) dayPage(w http.ResponseWriter, r *http.Request) {
	d, err := s.dayParam(r, chi.URLParam(r, "date"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	v, err := s.buildDay(r.Context(), d)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if isHTMX(r) {
		s.renderFragment(w, r, "day", v)
		return
	}
	s.renderPage(w, r, http.StatusOK, "day", v.Label, v)
}

// renderDay answers a mutation: the #day fragment for HTMX, a redirect for a
// plain form post.
func (s *Server) renderDay(w http.ResponseWriter, r *http.Request, d civil.Date, notice, errMsg, quickAdd string) {
	s.renderDayView(w, r, d, func(v *dayView) { v.Notice, v.Error, v.QuickAdd = notice, errMsg, quickAdd })
}

func (s *Server) renderDayView(w http.ResponseWriter, r *http.Request, d civil.Date, set func(*dayView)) {
	if !isHTMX(r) {
		http.Redirect(w, r, "/day/"+d.String(), http.StatusSeeOther)
		return
	}
	v, err := s.buildDay(r.Context(), d)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	set(&v)
	if v.Error == "" {
		w.Header().Set("HX-Trigger", "closeModal")
	}
	s.renderFragment(w, r, "day", v)
}

// --- JSON -----------------------------------------------------------------------

type jsonBlock struct {
	ActivityID int64           `json:"activity_id,omitempty"`
	Title      string          `json:"title,omitempty"`
	Kind       schedule.Kind   `json:"kind"`
	Start      string          `json:"start"`
	End        string          `json:"end"`
	Anchored   bool            `json:"anchored"`
	Flags      []schedule.Flag `json:"flags,omitempty"`
}

type jsonSplit struct {
	Task       int `json:"task"`
	Distractor int `json:"distractor"`
}

type jsonDay struct {
	Date    string      `json:"date"`
	Blocks  []jsonBlock `json:"blocks"`
	Summary struct {
		TaskMinutes       int                  `json:"task_minutes"`
		DistractorMinutes int                  `json:"distractor_minutes"`
		TotalMinutes      int                  `json:"total_minutes"`
		AvailableMinutes  int                  `json:"available_minutes"`
		FreeMinutes       int                  `json:"free_minutes"`
		DoneMinutes       int                  `json:"done_minutes"`
		RemainingMinutes  int                  `json:"remaining_minutes"`
		ByCompany         map[string]jsonSplit `json:"by_company"`
		ByProject         map[string]jsonSplit `json:"by_project"`
	} `json:"summary"`
}

func (s *Server) dayJSON(w http.ResponseWriter, r *http.Request) {
	d, err := civil.ParseDate(chi.URLParam(r, "date"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_, settings, err := s.today(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	acts, err := s.store.ListDay(r.Context(), d)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	titles := map[int64]string{}
	items := make([]schedule.Activity, len(acts))
	for i, a := range acts {
		items[i], titles[a.ID] = a.ScheduleItem(), a.Title
	}
	blocks, sum := schedule.BuildDay(d, items, settings.Schedule())

	out := jsonDay{Date: d.String(), Blocks: []jsonBlock{}}
	for _, b := range blocks {
		out.Blocks = append(out.Blocks, jsonBlock{
			ActivityID: b.ActivityID, Title: titles[b.ActivityID], Kind: b.Kind,
			Start: b.Start.String(), End: b.End.String(), Anchored: b.Anchored, Flags: b.Flags,
		})
	}
	o := &out.Summary
	o.TaskMinutes, o.DistractorMinutes, o.TotalMinutes = sum.TaskMinutes, sum.DistractorMinutes, sum.TotalMinutes
	o.AvailableMinutes, o.FreeMinutes = sum.AvailableMinutes, sum.FreeMinutes
	o.DoneMinutes, o.RemainingMinutes = sum.DoneMinutes, sum.RemainingMinutes
	o.ByCompany, o.ByProject = map[string]jsonSplit{}, map[string]jsonSplit{}
	for k, v := range sum.ByCompany {
		o.ByCompany[k] = jsonSplit(v)
	}
	for k, v := range sum.ByProject {
		o.ByProject[k] = jsonSplit(v)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}
