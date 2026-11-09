package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/ocalderonc/omnitodo/internal/civil"
	"github.com/ocalderonc/omnitodo/internal/schedule"
	"github.com/ocalderonc/omnitodo/internal/voice"
)

// vocabulary is what the voice models are told: active company, project and
// tag names, under short references.
func (s *Server) vocabulary(ctx context.Context) (voice.Vocabulary, error) {
	var v voice.Vocabulary
	companies, err := s.store.ListCompanies(ctx, false)
	if err != nil {
		return v, err
	}
	refs := map[int64]voice.Named{}
	for i, c := range companies {
		n := voice.Named{Ref: "c" + strconv.Itoa(i+1), ID: c.ID, Name: c.Name}
		refs[c.ID] = n
		v.Companies = append(v.Companies, n)
	}
	projects, err := s.store.ListProjects(ctx, false)
	if err != nil {
		return v, err
	}
	for i, p := range projects {
		n := voice.Named{Ref: "p" + strconv.Itoa(i+1), ID: p.ID, Name: p.Name}
		if p.CompanyID != nil {
			if c, ok := refs[*p.CompanyID]; ok {
				n.CompanyRef, n.Company = c.Ref, c.Name
			}
		}
		v.Projects = append(v.Projects, n)
	}
	tags, err := s.store.ListTags(ctx)
	if err != nil {
		return v, err
	}
	for _, t := range tags {
		v.Tags = append(v.Tags, t.Name)
	}
	return v, nil
}

// voiceDraft takes a recording (field "audio") or typed text (field "text")
// and answers with the New activity form, filled with what was heard. It
// never saves: the owner reads the form and taps Save.
func (s *Server) voiceDraft(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Voice == nil {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, voice.MaxAudioBytes+64<<10)
	if err := r.ParseMultipartForm(1 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		// The day also comes in the query string, so it survives a body
		// that could not be read.
		day, _ := s.formDay(r)
		s.voiceForm(w, r, day, voice.Heard{}, errors.New("That recording is too long: keep it to one activity."))
		return
	}
	day, err := s.formDay(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	vocab, err := s.vocabulary(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	today, _, err := s.today(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	var heard voice.Heard
	if file, header, ferr := r.FormFile("audio"); ferr == nil {
		data, rerr := io.ReadAll(file)
		file.Close()
		if rerr != nil {
			s.voiceForm(w, r, day, heard, errors.New("That recording is too long: keep it to one activity."))
			return
		}
		heard, err = s.cfg.Voice.Hear(ctx, vocab, voice.Audio{Data: data, Filename: audioFilename(header.Filename)}, today)
	} else {
		heard, err = s.cfg.Voice.Read(ctx, vocab, r.FormValue("text"), today)
	}
	if err != nil {
		// The error carries a status code at most, never what was said.
		s.log.Warn("voice draft failed", "err", err)
	}
	s.voiceForm(w, r, day, heard, err)
}

// audioFilename keeps only a known extension: the provider tells the format
// by it, and nothing else of the client's filename is passed on.
func audioFilename(name string) string {
	for _, ext := range []string{".m4a", ".mp4", ".webm", ".ogg", ".wav", ".mp3"} {
		if strings.HasSuffix(strings.ToLower(name), ext) {
			return "voice" + ext
		}
	}
	return "voice.webm"
}

func voiceMessage(err error) string {
	switch {
	case errors.Is(err, voice.ErrInput):
		return capitalize(strings.TrimPrefix(err.Error(), voice.ErrInput.Error()+": ")) + "."
	case errors.Is(err, voice.ErrBusy):
		return "Too many voice entries in the last hour. Try again later, or fill the form."
	case errors.Is(err, voice.ErrTranscription):
		return "Could not transcribe that. Try again, or fill the form."
	case errors.Is(err, voice.ErrUpstream):
		return "Could not read that. Try again, or fill the form."
	case errors.Is(err, voice.ErrDisabled):
		return "Voice entry is not set up on this server."
	}
	return err.Error()
}

func (s *Server) voiceForm(w http.ResponseWriter, r *http.Request, day civil.Date, heard voice.Heard, err error) {
	d := heard.Draft
	v := formView{Day: day, Kind: string(schedule.KindTask), Duration: "30m", Heard: heard.Transcript, Voice: true}
	if err != nil {
		v.Error = voiceMessage(err)
	} else {
		v.Title, v.Kind, v.Notes = d.Title, d.Kind, d.Notes
		if d.DurationMin > 0 {
			v.Duration = compactDuration(d.DurationMin)
		} else {
			v.Notes = append(v.Notes, "No duration heard: 30m by default.")
		}
		if d.Date != nil {
			v.Day = *d.Date
		}
		if d.StartAt != nil {
			v.StartAt = d.StartAt.String()
		}
		if d.DueDate != nil {
			v.DueDate = d.DueDate.String()
		}
		switch {
		case d.ProjectID != nil:
			v.Owner = "p:" + strconv.FormatInt(*d.ProjectID, 10)
		case d.CompanyID != nil:
			v.Owner = "c:" + strconv.FormatInt(*d.CompanyID, 10)
		}
		v.Tags = strings.Join(d.Tags, ", ")
		if v.Title == "" {
			v.Notes = append(v.Notes, "No title heard: write one.")
		}
	}
	if v.Owners, err = s.ownerGroups(r, "", ""); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderFragment(w, r, "form", v)
}
