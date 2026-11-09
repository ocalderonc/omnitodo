package voice

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ocalderonc/omnitodo/internal/civil"
)

var today = civil.NewDate(2026, 11, 9) // a Monday

var vocab = Vocabulary{
	Companies: []Named{{Ref: "c1", ID: 10, Name: "Fox Nation"}, {Ref: "c2", ID: 20, Name: "Éxito"}},
	Projects: []Named{
		{Ref: "p1", ID: 100, Name: "LG", CompanyRef: "c1", Company: "Fox Nation"},
		{Ref: "p2", ID: 200, Name: "Web", CompanyRef: "c2", Company: "Éxito"},
		{Ref: "p3", ID: 300, Name: "Web", CompanyRef: "c1", Company: "Fox Nation"},
		{Ref: "p4", ID: 400, Name: "Casa"},
	},
	Tags: []string{"review", "Reunión"},
}

func ptr[T any](v T) *T { return &v }

func TestCheckTheExampleSentence(t *testing.T) {
	// "Enviar deployment de tal cosa bajo el proyecto LG de la empresa Fox
	// Nation que dura 20 minutos y debo terminar a las cuatro pm"
	d := Check(Extraction{
		Title: "enviar deployment de tal cosa", Kind: "task", DurationMin: "20",
		ProjectRef: "p1", Project: "LG", CompanyRef: "c1", Company: "Fox Nation", End: "16:00",
	}, vocab, today)
	if d.Title != "Enviar deployment de tal cosa" || d.Kind != "task" || d.DurationMin != 20 {
		t.Errorf("draft: %+v", d)
	}
	if d.ProjectID == nil || *d.ProjectID != 100 || d.CompanyID != nil {
		t.Errorf("owner: project %v company %v", d.ProjectID, d.CompanyID)
	}
	if d.StartAt == nil || d.StartAt.String() != "15:40" {
		t.Errorf("start worked back from the end: %v", d.StartAt)
	}
	if len(d.Notes) != 1 || !strings.Contains(d.Notes[0], "15:40") {
		t.Errorf("notes: %q", d.Notes)
	}
}

func TestCheckOwner(t *testing.T) {
	for name, tc := range map[string]struct {
		x        Extraction
		project  int64
		company  int64
		noteHint string
	}{
		"company only":               {x: Extraction{CompanyRef: "c2", Company: "Éxito"}, company: 20},
		"company by name, any case":  {x: Extraction{Company: "éxito"}, company: 20},
		"project by name in company": {x: Extraction{Project: "web", Company: "Fox Nation"}, project: 300},
		"ambiguous project name":     {x: Extraction{Project: "web"}, noteHint: "could not place"},
		"project of another company": {x: Extraction{ProjectRef: "p2", CompanyRef: "c1"}, noteHint: "belongs to Éxito"},
		"unknown project in company": {x: Extraction{CompanyRef: "c1", Project: "Nope"}, company: 10, noteHint: "no project by that name"},
		"invented ref":               {x: Extraction{ProjectRef: "p99"}},
		"project without company ok": {x: Extraction{ProjectRef: "p4"}, project: 400},
		"nothing":                    {x: Extraction{}},
	} {
		d := Check(tc.x, vocab, today)
		if got := deref(d.ProjectID); got != tc.project {
			t.Errorf("%s: project %d, want %d", name, got, tc.project)
		}
		if got := deref(d.CompanyID); got != tc.company {
			t.Errorf("%s: company %d, want %d", name, got, tc.company)
		}
		notes := strings.Join(d.Notes, " ")
		if (tc.noteHint == "") != (notes == "") || !strings.Contains(notes, tc.noteHint) {
			t.Errorf("%s: notes %q, want %q", name, notes, tc.noteHint)
		}
	}
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func TestCheckFields(t *testing.T) {
	d := Check(Extraction{
		Title: "  almuerzo  ", Kind: "Distractor", DurationMin: "45.4", Date: "2026-11-10",
		Start: "13:00", End: "14:00", DueDate: "2026-11-13",
		Tags: []string{"REVIEW", "invented"}, NewTags: []string{"#nuevo cliente", "reunión"},
	}, vocab, today)
	if d.Title != "Almuerzo" || d.Kind != "distractor" || d.DurationMin != 45 {
		t.Errorf("basics: %+v", d)
	}
	if d.Date == nil || d.Date.String() != "2026-11-10" || d.DueDate == nil || d.DueDate.String() != "2026-11-13" {
		t.Errorf("dates: %v %v", d.Date, d.DueDate)
	}
	if d.StartAt == nil || d.StartAt.String() != "13:00" || !strings.Contains(strings.Join(d.Notes, ""), "kept 45 minutes") {
		t.Errorf("start/end vs duration: %v %q", d.StartAt, d.Notes)
	}
	if strings.Join(d.Tags, ",") != "review,nuevo-cliente,Reunión" {
		t.Errorf("tags: %q", d.Tags)
	}

	// Start and end without a duration: the duration is the gap.
	d = Check(Extraction{Start: "9:00", End: "10:30"}, vocab, today)
	if d.DurationMin != 90 || d.StartAt.String() != "09:00" {
		t.Errorf("from start and end: %+v", d)
	}
	// Garbage is dropped, not repaired.
	d = Check(Extraction{Kind: "meeting", DurationMin: "-5", Date: "2026-10-01", Start: "25:00", DueDate: "Friday"}, vocab, today)
	if d.Kind != "task" || d.DurationMin != 0 || d.Date != nil || d.StartAt != nil || d.DueDate != nil {
		t.Errorf("garbage kept: %+v", d)
	}
	// An end too early to fit the duration has no start before midnight.
	if d := Check(Extraction{DurationMin: "60", End: "00:30"}, vocab, today); d.StartAt != nil {
		t.Errorf("start before midnight: %v", d.StartAt)
	}
}

func TestLLMRequestAndDecode(t *testing.T) {
	var got struct {
		Model          string            `json:"model"`
		ResponseFormat map[string]string `json:"response_format"`
		Messages       []chatMessage     `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("request: %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"title\":\"Enviar deployment\",\"duration_min\":20,\"project\":\"p1\",\"end\":\"16:00\",\"tags\":[\"review\"],\"new_tags\":null}"}}]}`)
	}))
	defer srv.Close()
	x, err := NewLLM(srv.URL+"/", "k", "deepseek-flash", nil).Extract(context.Background(), vocab, "enviar deployment", today)
	if err != nil {
		t.Fatal(err)
	}
	if x.Title != "Enviar deployment" || x.DurationMin != "20" || x.ProjectRef != "p1" || x.End != "16:00" || len(x.Tags) != 1 {
		t.Errorf("extraction: %+v", x)
	}
	if got.Model != "deepseek-flash" || got.ResponseFormat["type"] != "json_object" || len(got.Messages) != 3 {
		t.Fatalf("request body: %+v", got)
	}
	if v := got.Messages[1].Content; !strings.Contains(v, "- p1: LG (company c1: Fox Nation)") || !strings.Contains(v, "- p4: Casa (no company)") {
		t.Errorf("vocabulary message:\n%s", v)
	}
	if u := got.Messages[2].Content; !strings.HasPrefix(u, "Today is Monday, 2026-11-09.") {
		t.Errorf("user message: %s", u)
	}
}

func TestLLMFailures(t *testing.T) {
	for name, body := range map[string]string{
		"status":     "",
		"empty":      `{"choices":[{"message":{"content":""},"finish_reason":"length"}]}`,
		"not object": `{"choices":[{"message":{"content":"[1,2]"}}]}`,
		"no choices": `{"choices":[]}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if body == "" {
				http.Error(w, "quota for: enviar deployment", http.StatusTooManyRequests)
				return
			}
			io.WriteString(w, body)
		}))
		_, err := NewLLM(srv.URL, "k", "m", nil).Extract(context.Background(), vocab, "enviar deployment", today)
		srv.Close()
		if !errors.Is(err, ErrUpstream) || strings.Contains(err.Error(), "enviar") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTranscriber(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		f, h, _ := r.FormFile("file")
		data, _ := io.ReadAll(f)
		if r.URL.Path != "/v1/audio/transcriptions" || r.FormValue("model") != "gpt-4o-transcribe" ||
			h.Filename != "voice.m4a" || string(data) != "AUDIO" || !strings.Contains(r.FormValue("prompt"), "Fox Nation, Éxito, LG") {
			t.Errorf("request: %s %v %s %q", r.URL.Path, r.Form, h.Filename, data)
		}
		io.WriteString(w, `{"text":"  Enviar deployment  "}`)
	}))
	defer srv.Close()
	text, err := NewTranscriber(srv.URL+"/v1", "k", "gpt-4o-transcribe", nil).
		Transcribe(context.Background(), Audio{Data: []byte("AUDIO"), Filename: "voice.m4a"}, vocab.Names())
	if err != nil || text != "Enviar deployment" {
		t.Errorf("transcript %q, %v", text, err)
	}
}

type fakeLLM struct {
	x     Extraction
	calls int
}

func (f *fakeLLM) Extract(context.Context, Vocabulary, string, civil.Date) (Extraction, error) {
	f.calls++
	return f.x, nil
}

type fakeSTT struct{ text string }

func (f fakeSTT) Transcribe(context.Context, Audio, []string) (string, error) { return f.text, nil }

func TestServiceLimitsAndInput(t *testing.T) {
	llm := &fakeLLM{x: Extraction{Title: "x"}}
	s := NewService(llm, fakeSTT{text: "  "})
	now := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	ctx := context.Background()

	if _, err := s.Hear(ctx, vocab, Audio{}, today); !errors.Is(err, ErrInput) {
		t.Errorf("empty audio: %v", err)
	}
	if _, err := s.Hear(ctx, vocab, Audio{Data: make([]byte, MaxAudioBytes+1)}, today); !errors.Is(err, ErrInput) {
		t.Errorf("huge audio: %v", err)
	}
	if _, err := s.Hear(ctx, vocab, Audio{Data: []byte("a")}, today); !errors.Is(err, ErrInput) || llm.calls != 0 {
		t.Errorf("silent transcript must not reach the model: %v, %d calls", err, llm.calls)
	}
	if _, err := s.Read(ctx, vocab, strings.Repeat("a", 501), today); !errors.Is(err, ErrInput) {
		t.Errorf("long text: %v", err)
	}
	for i := 0; i < perHour; i++ {
		s.Read(ctx, vocab, "hola", today)
	}
	if _, err := s.Read(ctx, vocab, "hola", today); !errors.Is(err, ErrBusy) {
		t.Errorf("over the cap: %v", err)
	}
	now = now.Add(61 * time.Minute)
	if _, err := s.Read(ctx, vocab, "hola", today); err != nil {
		t.Errorf("after an hour: %v", err)
	}
	if NewService(llm, nil).Transcribes() || (*Service)(nil).Transcribes() {
		t.Error("Transcribes without a transcriber")
	}
}
