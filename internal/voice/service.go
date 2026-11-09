package voice

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ocalderonc/omnitodo/internal/civil"
)

const (
	maxTranscriptRunes = 500
	maxTitleRunes      = 80
	maxTags            = 10
	maxTagRunes        = 40
	maxDurationMin     = 24 * 60
	// perHour caps calls. Far above anyone planning their day by voice, low
	// enough that a stolen session cannot run up the provider bill.
	perHour = 60
)

type extractor interface {
	Extract(ctx context.Context, v Vocabulary, transcript string, today civil.Date) (Extraction, error)
}

type transcriber interface {
	Transcribe(ctx context.Context, audio Audio, names []string) (string, error)
}

// Service validates input, calls the models, and checks what they said.
type Service struct {
	llm extractor
	stt transcriber
	now func() time.Time

	mu    sync.Mutex
	calls []time.Time
}

// NewService needs a language model; stt may be nil, and then only typed
// text is read.
func NewService(llm extractor, stt transcriber) *Service {
	return &Service{llm: llm, stt: stt, now: time.Now}
}

// Transcribes reports whether audio is accepted.
func (s *Service) Transcribes() bool { return s != nil && s.stt != nil }

// Hear transcribes a recording and reads it.
func (s *Service) Hear(ctx context.Context, v Vocabulary, audio Audio, today civil.Date) (Heard, error) {
	if !s.Transcribes() {
		return Heard{}, ErrDisabled
	}
	switch {
	case len(audio.Data) == 0:
		return Heard{}, fmt.Errorf("%w: nothing was recorded", ErrInput)
	case len(audio.Data) > MaxAudioBytes:
		return Heard{}, fmt.Errorf("%w: keep it to one activity, under half a minute", ErrInput)
	}
	// One allowance for both calls: a recording is one entry.
	if !s.allow() {
		return Heard{}, ErrBusy
	}
	transcript, err := s.stt.Transcribe(ctx, audio, v.Names())
	if err != nil {
		return Heard{}, err
	}
	transcript = squeeze(transcript)
	if transcript == "" {
		return Heard{}, fmt.Errorf("%w: nothing was heard", ErrInput)
	}
	if utf8.RuneCountInString(transcript) > maxTranscriptRunes {
		transcript = string([]rune(transcript)[:maxTranscriptRunes])
	}
	d, err := s.read(ctx, v, transcript, today)
	return Heard{Transcript: transcript, Draft: d}, err
}

// Read reads typed text, for the same draft without a recording.
func (s *Service) Read(ctx context.Context, v Vocabulary, text string, today civil.Date) (Heard, error) {
	if s == nil || s.llm == nil {
		return Heard{}, ErrDisabled
	}
	text = squeeze(text)
	switch n := utf8.RuneCountInString(text); {
	case n == 0:
		return Heard{}, fmt.Errorf("%w: say what the activity is", ErrInput)
	case n > maxTranscriptRunes:
		return Heard{}, fmt.Errorf("%w: keep it under 500 characters", ErrInput)
	}
	if !s.allow() {
		return Heard{}, ErrBusy
	}
	d, err := s.read(ctx, v, text, today)
	return Heard{Transcript: text, Draft: d}, err
}

func (s *Service) read(ctx context.Context, v Vocabulary, transcript string, today civil.Date) (Draft, error) {
	x, err := s.llm.Extract(ctx, v, transcript, today)
	if err != nil {
		return Draft{}, err
	}
	return Check(x, v, today), nil
}

// Check keeps what the model said only where it is what it claims to be, and
// resolves names to the owner's own IDs. Pure, so the rules are tested
// without a model.
func Check(x Extraction, v Vocabulary, today civil.Date) Draft {
	d := Draft{Title: normaliseTitle(x.Title), Kind: "task"}
	if strings.EqualFold(x.Kind, "distractor") {
		d.Kind = "distractor"
	}
	if n, err := strconv.ParseFloat(strings.TrimSpace(x.DurationMin), 64); err == nil && n >= 1 && n <= maxDurationMin {
		d.DurationMin = int(n + 0.5)
	}
	if day, err := civil.ParseDate(x.Date); err == nil && !day.Before(today.AddDays(-7)) {
		d.Date = &day // a week back at most: older is a mishearing
	}
	if day, err := civil.ParseDate(x.DueDate); err == nil {
		d.DueDate = &day
	}

	start, startOK := parseClock(x.Start)
	end, endOK := parseClock(x.End)
	switch {
	case startOK && endOK && end > start:
		d.StartAt = &start
		if d.DurationMin == 0 {
			d.DurationMin = int(end - start)
		} else if int(end-start) != d.DurationMin {
			d.Notes = append(d.Notes, fmt.Sprintf("Heard %s to %s but also %d minutes: kept %d minutes.", start, end, d.DurationMin, d.DurationMin))
		}
	case startOK:
		d.StartAt = &start
	case endOK:
		// "Must finish at 16:00": the planner has a start, not an end, so the
		// start is worked back from the duration.
		dur := d.DurationMin
		if dur == 0 {
			dur = 30
		}
		if s := end.Add(-dur); s >= 0 {
			d.StartAt = &s
			d.Notes = append(d.Notes, fmt.Sprintf("Fixed start %s, so it ends at %s.", s, end))
		}
	}

	place(x, v, &d)
	d.Tags = tagsOf(x, v)
	return d
}

// place resolves the project and company: by the references the model
// picked, then by the names it heard. What still matches nothing, or more
// than one thing, is left empty and noted.
func place(x Extraction, v Vocabulary, d *Draft) {
	var company *Named
	if c := find(v.Companies, x.CompanyRef); c != nil {
		company = c
	} else if x.Company != "" {
		if cs := byName(v.Companies, x.Company); len(cs) == 1 {
			company = &cs[0]
		}
	}

	var project *Named
	if p := find(v.Projects, x.ProjectRef); p != nil {
		project = p
	} else if x.Project != "" {
		candidates := byName(v.Projects, x.Project)
		if company != nil {
			var inCompany []Named
			for _, p := range candidates {
				if p.CompanyRef == company.Ref {
					inCompany = append(inCompany, p)
				}
			}
			candidates = inCompany
		}
		if len(candidates) == 1 {
			project = &candidates[0]
		}
	}

	switch {
	case project != nil:
		if company != nil && project.CompanyRef != company.Ref {
			// "Project X of company Y" where X belongs elsewhere: neither is
			// trusted.
			d.Notes = append(d.Notes, fmt.Sprintf("Heard project “%s” under company “%s”, but it belongs to %s: choose one.",
				project.Name, company.Name, orNone(project.Company)))
			return
		}
		id := project.ID
		d.ProjectID = &id
	case company != nil:
		id := company.ID
		d.CompanyID = &id
		if x.Project != "" {
			d.Notes = append(d.Notes, fmt.Sprintf("Heard project “%s” but %s has no project by that name.", x.Project, company.Name))
		}
	default:
		if x.Project != "" {
			d.Notes = append(d.Notes, fmt.Sprintf("Heard project “%s” but could not place it.", x.Project))
		}
		if x.Company != "" {
			d.Notes = append(d.Notes, fmt.Sprintf("Heard company “%s” but could not place it.", x.Company))
		}
	}
}

func orNone(company string) string {
	if company == "" {
		return "no company"
	}
	return company
}

// tagsOf keeps existing tags in the owner's spelling, then new ones only when
// explicitly asked for. A tag the model claims exists but does not is dropped.
func tagsOf(x Extraction, v Vocabulary) []string {
	known := map[string]string{}
	for _, t := range v.Tags {
		known[strings.ToLower(t)] = t
	}
	var out []string
	seen := map[string]bool{}
	add := func(t string) {
		k := strings.ToLower(t)
		if t != "" && !seen[k] && len(out) < maxTags {
			seen[k] = true
			out = append(out, t)
		}
	}
	for _, t := range x.Tags {
		if name, ok := known[strings.ToLower(tagWord(t))]; ok {
			add(name)
		}
	}
	for _, t := range x.NewTags {
		t = tagWord(t)
		if name, ok := known[strings.ToLower(t)]; ok {
			add(name)
		} else if utf8.RuneCountInString(t) <= maxTagRunes {
			add(t)
		}
	}
	return out
}

// tagWord makes a tag one token, as the form's tag field splits on spaces.
func tagWord(t string) string {
	return strings.Join(strings.Fields(strings.TrimPrefix(strings.TrimSpace(t), "#")), "-")
}

func parseClock(raw string) (civil.Time, bool) {
	t, err := civil.ParseTime(strings.TrimSpace(raw))
	return t, err == nil
}

func squeeze(s string) string { return strings.Join(strings.Fields(s), " ") }

func normaliseTitle(raw string) string {
	title := squeeze(raw)
	if utf8.RuneCountInString(title) > maxTitleRunes {
		title = strings.TrimSpace(string([]rune(title)[:maxTitleRunes]))
	}
	if title == "" {
		return ""
	}
	first, size := utf8.DecodeRuneInString(title)
	return string(unicode.ToUpper(first)) + title[size:]
}

// allow records a call and reports whether it is within the hourly cap. In
// memory: one user, one process; a guard on spend, not an accounting of it.
func (s *Service) allow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	recent := s.calls[:0]
	for _, at := range s.calls {
		if at.After(now.Add(-time.Hour)) {
			recent = append(recent, at)
		}
	}
	s.calls = recent
	if len(recent) >= perHour {
		return false
	}
	s.calls = append(s.calls, now)
	return true
}
