package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ocalderonc/omnitodo/internal/auth"
	"github.com/ocalderonc/omnitodo/internal/store"
	"github.com/ocalderonc/omnitodo/internal/voice"
)

// fakeAPIs stands in for OpenAI (transcription) and DeepSeek (chat): the
// transcript and the model's JSON answer are given.
func fakeAPIs(t *testing.T, transcript, answer string) (*httptest.Server, *[]string) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/audio/transcriptions":
			r.ParseMultipartForm(1 << 20)
			seen = append(seen, "stt:"+r.FormValue("prompt"))
			json.NewEncoder(w).Encode(map[string]string{"text": transcript})
		case "/chat/completions":
			b, _ := io.ReadAll(r.Body)
			seen = append(seen, "llm:"+string(b))
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": answer}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func newVoiceEnv(t *testing.T, svc *voice.Service) *env {
	t.Helper()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sessions, _ := auth.NewSessions(strings.Repeat("k", 40), time.Hour)
	h, err := New(st, Config{PassphraseHash: hashOnce(), Sessions: sessions, Now: func() time.Time { return now }, Voice: svc})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	e := &env{t: t, store: st, srv: srv}
	e.login()
	return e
}

func (e *env) postAudio(day string, audio []byte) resp {
	e.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("day", day)
	fw, _ := mw.CreateFormFile("audio", "../../etc/passwd.m4a")
	fw.Write(audio)
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/voice?day="+day, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", e.srv.URL)
	req.Header.Set("HX-Request", "true")
	req.AddCookie(e.cookie)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return resp{r.StatusCode, r.Header, string(b)}
}

func TestVoiceFillsTheForm(t *testing.T) {
	api, seen := fakeAPIs(t,
		"Enviar deployment de la API bajo el proyecto LG de Fox Nation, dura 20 minutos y debo terminar a las cuatro pm",
		`{"title":"Enviar deployment de la API","kind":"task","duration_min":20,"project":"p1","project_heard":"LG","company":"c1","company_heard":"Fox Nation","date":null,"start":null,"end":"16:00","due_date":null,"tags":[],"new_tags":[]}`)
	svc := voice.NewService(voice.NewLLM(api.URL, "k", "m", nil), voice.NewTranscriber(api.URL+"/v1", "k", "m", nil))
	e := newVoiceEnv(t, svc)
	fox, _ := e.store.CreateCompany(ctx, store.Company{Name: "Fox Nation"})
	lg, _ := e.store.CreateProject(ctx, store.Project{Name: "LG", CompanyID: &fox.ID})

	// The day page offers the microphone (hidden until the script sees one).
	if r := e.do(http.MethodGet, "/day", nil, false); !strings.Contains(r.body, `data-voice hidden`) {
		t.Error("no microphone button")
	}
	if r := e.do(http.MethodGet, "/day", nil, false); !strings.Contains(r.header.Get("Permissions-Policy"), "microphone=(self)") {
		t.Errorf("Permissions-Policy: %s", r.header.Get("Permissions-Policy"))
	}

	r := e.postAudio(today.String(), []byte("AUDIO"))
	if r.status != 200 {
		t.Fatalf("voice: %d %s", r.status, r.body)
	}
	for _, want := range []string{
		`Heard: “Enviar deployment de la API bajo el proyecto LG`,
		`name="title" value="Enviar deployment de la API"`,
		`name="duration" value="20m"`,
		`name="start_at" value="15:40"`,
		`selected>LG</option>`,
		`Fixed start 15:40, so it ends at 16:00.`,
		`hx-post="/activities"`,
	} {
		if !strings.Contains(r.body, want) {
			t.Errorf("form lacks %q:\n%s", want, r.body)
		}
	}
	if acts, _ := e.store.ListDay(ctx, today); len(acts) != 0 {
		t.Fatal("voice saved an activity by itself")
	}
	// The transcription prompt carried the names; the model got them as refs.
	if len(*seen) != 2 || !strings.Contains((*seen)[0], "Fox Nation") || !strings.Contains((*seen)[1], `p1: LG (company c1: Fox Nation)`) {
		t.Errorf("provider calls: %q", *seen)
	}

	// Saving the form is the ordinary form post.
	pid := "p:" + strconv.FormatInt(lg.ID, 10)
	e.do(http.MethodPost, "/activities", url.Values{"form": {"1"}, "day": {today.String()}, "title": {"Enviar deployment de la API"},
		"kind": {"task"}, "duration": {"20m"}, "start_at": {"15:40"}, "owner": {pid}}, true)
	if acts, _ := e.store.ListDay(ctx, today); len(acts) != 1 || acts[0].ProjectName != "LG" || acts[0].StartAt.String() != "15:40" {
		t.Errorf("saved: %+v", acts)
	}
}

func TestVoiceErrorsStillGiveAForm(t *testing.T) {
	api, _ := fakeAPIs(t, "", `{}`)
	svc := voice.NewService(voice.NewLLM(api.URL, "k", "m", nil), voice.NewTranscriber(api.URL+"/v1", "k", "m", nil))
	e := newVoiceEnv(t, svc)
	r := e.postAudio(today.String(), []byte("AUDIO"))
	if r.status != 200 || !strings.Contains(r.body, "Nothing was heard.") || !strings.Contains(r.body, `name="title"`) {
		t.Errorf("empty transcript: %d\n%s", r.status, r.body)
	}

	broken := voice.NewService(voice.NewLLM("http://127.0.0.1:1", "k", "m", nil), voice.NewTranscriber("http://127.0.0.1:1", "k", "m", nil))
	e = newVoiceEnv(t, broken)
	if r := e.postAudio(today.String(), []byte("AUDIO")); !strings.Contains(r.body, "Could not transcribe that") {
		t.Errorf("provider down:\n%s", r.body)
	}
}

func TestVoiceOff(t *testing.T) {
	e := newEnv(t)
	e.login()
	if r := e.do(http.MethodGet, "/day", nil, false); strings.Contains(r.body, "data-voice") {
		t.Error("microphone shown with voice off")
	}
	if r := e.postAudio(today.String(), []byte("AUDIO")); r.status != http.StatusNotFound {
		t.Errorf("voice off: %d", r.status)
	}

	// A language model without transcription reads typed text, but offers no
	// microphone.
	api, _ := fakeAPIs(t, "", `{"title":"Llamar a Ana","company_heard":"Nope","duration_min":15}`)
	e = newVoiceEnv(t, voice.NewService(voice.NewLLM(api.URL, "k", "m", nil), nil))
	if r := e.do(http.MethodGet, "/day", nil, false); strings.Contains(r.body, "data-voice") {
		t.Error("microphone shown without transcription")
	}
	r := e.do(http.MethodPost, "/voice", url.Values{"text": {"llamar a Ana 15 minutos para Nope"}, "day": {today.String()}}, true)
	if !strings.Contains(r.body, `value="Llamar a Ana"`) || !strings.Contains(r.body, `Heard company “Nope” but could not place it.`) {
		t.Errorf("text path:\n%s", r.body)
	}
}

func TestVoiceNeedsSession(t *testing.T) {
	api, seen := fakeAPIs(t, "x", `{}`)
	svc := voice.NewService(voice.NewLLM(api.URL, "k", "m", nil), voice.NewTranscriber(api.URL+"/v1", "k", "m", nil))
	e := newVoiceEnv(t, svc)
	e.cookie = nil
	if r := e.do(http.MethodPost, "/voice", url.Values{"text": {"hola"}}, true); r.status != http.StatusUnauthorized || len(*seen) != 0 {
		t.Errorf("no session: %d, %d provider calls", r.status, len(*seen))
	}
}
