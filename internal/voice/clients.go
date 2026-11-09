package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ocalderonc/omnitodo/internal/civil"
)

// maxResponseBytes bounds what is read back; an answer is a few hundred bytes.
const maxResponseBytes = 1 << 20

// MaxAudioBytes bounds one recording: half a minute of compressed speech is a
// few hundred kilobytes.
const MaxAudioBytes = 4 << 20

// systemPrompt is fixed and first, so a provider that caches prompt prefixes
// (DeepSeek does) charges the cache rate for it after the first call. The
// names follow in a second system message, the date and sentence last.
const systemPrompt = `You read one sentence someone said about a task for their daily planner and extract a single activity from it.
The sentence may be in Spanish, English or both, and may have been transcribed imperfectly. Reply with a JSON object and nothing else, with exactly these keys:

{"title": "Enviar deployment del API", "kind": "task", "duration_min": 20, "project": "p3", "project_heard": "xyz", "company": "c2", "company_heard": "abc", "date": null, "start": null, "end": "16:00", "due_date": null, "tags": [], "new_tags": []}

You are also given the person's companies and projects, each with a reference such as "c2" or "p3" (a project lists the company it belongs to), and the tags they already use.

Rules:
- Use null for anything the sentence does not say. Never guess or fill a default.
- title: what has to be done, as a short phrase in the language spoken, at most 8 words, starting with a capital letter. Leave out the project, company, duration, times and dates ("Enviar deployment de la API", "Review the invoice").
- kind: "distractor" only when the person calls it a distractor, distraction or interruption ("distractor", "distracción"), or it is clearly not work: lunch, a break, picking someone up, commuting, the gym. Otherwise "task".
- duration_min: how long it takes, as a whole number of minutes ("20 minutos" is 20, "una hora y media" is 90, "half an hour" is 30). null when not said.
- project: the reference of the project it is for, from the projects listed, allowing for transcription mistakes. null when none is clearly meant.
- project_heard: the project as the sentence said it, or null.
- company: the reference of the company ("empresa", "compañía", "cliente") it is for, from the companies listed. When a project is chosen, its own company or null. When only a company is said ("bajo la empresa abc", "para abc"), that company and project null.
- company_heard: the company as the sentence said it, or null.
- date: YYYY-MM-DD, the day it will be done, only when a day is said ("mañana", "el viernes", "tomorrow"). Work relative days out from today's date, given below; a weekday means the next one, today included. null when no day is said.
- start: HH:MM, 24-hour, only when a start time is said ("a las 3 de la tarde" is "15:00", "starts at 9" is "09:00").
- end: HH:MM, 24-hour, only when the time it must be finished by is said ("debo terminar a las cuatro pm" is "16:00", "termina a las 5", "done by 11am").
- due_date: YYYY-MM-DD, only when a deadline day is said, different from the day it is done ("hay que entregarlo el viernes", "vence el 15", "due Friday").
- tags: the tags from the tags listed that the sentence names ("con etiqueta revisión", "tag review"). Only when the sentence names one; never inferred. [] when none.
- new_tags: tags the sentence explicitly asks to create ("etiqueta nueva cliente", "new tag ops"), as said, one word each when possible. Never one already listed. [] when none.`

// vocabularyMessage lists the names under their references.
func vocabularyMessage(v Vocabulary) string {
	var b strings.Builder
	b.WriteString("Companies:\n")
	if len(v.Companies) == 0 {
		b.WriteString("(none)\n")
	}
	for _, c := range v.Companies {
		fmt.Fprintf(&b, "- %s: %s\n", c.Ref, c.Name)
	}
	b.WriteString("Projects:\n")
	if len(v.Projects) == 0 {
		b.WriteString("(none)\n")
	}
	for _, p := range v.Projects {
		if p.Company != "" {
			fmt.Fprintf(&b, "- %s: %s (company %s: %s)\n", p.Ref, p.Name, p.CompanyRef, p.Company)
		} else {
			fmt.Fprintf(&b, "- %s: %s (no company)\n", p.Ref, p.Name)
		}
	}
	b.WriteString("Tags:\n")
	if len(v.Tags) == 0 {
		b.WriteString("(none)\n")
	}
	for _, t := range v.Tags {
		fmt.Fprintf(&b, "- %s\n", t)
	}
	return strings.TrimRight(b.String(), "\n")
}

func userMessage(transcript string, today civil.Date) string {
	return fmt.Sprintf("Today is %s, %s.\nSentence: %s", today.Weekday(), today, transcript)
}

// LLM calls an OpenAI-compatible chat completions API.
type LLM struct {
	baseURL, apiKey, model string
	http                   *http.Client
}

// NewLLM builds a client for baseURL, e.g. https://api.deepseek.com. A nil
// httpClient gets one with a timeout, the backstop for a host that accepts
// the connection and then says nothing.
func NewLLM(baseURL, apiKey, model string, httpClient *http.Client) *LLM {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &LLM{strings.TrimRight(baseURL, "/"), apiKey, model, httpClient}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string            `json:"model"`
	Messages       []chatMessage     `json:"messages"`
	ResponseFormat map[string]string `json:"response_format"`
	// DeepSeek's reasoning switch: off, this is extraction. Providers that do
	// not know it ignore it.
	Thinking    map[string]string `json:"thinking,omitempty"`
	Temperature float64           `json:"temperature"`
	MaxTokens   int               `json:"max_tokens"`
}

// Extract asks the model to read transcript, with today as the owner's date.
func (c *LLM) Extract(ctx context.Context, v Vocabulary, transcript string, today civil.Date) (Extraction, error) {
	body, err := json.Marshal(chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "system", Content: vocabularyMessage(v)},
			{Role: "user", Content: userMessage(transcript, today)},
		},
		ResponseFormat: map[string]string{"type": "json_object"},
		Thinking:       map[string]string{"type": "disabled"},
		MaxTokens:      400,
	})
	if err != nil {
		return Extraction{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Extraction{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	raw, err := do(c.http, req, ErrUpstream)
	if err != nil {
		return Extraction{}, err
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Extraction{}, fmt.Errorf("%w: decode response: %v", ErrUpstream, err)
	}
	if len(parsed.Choices) == 0 {
		return Extraction{}, fmt.Errorf("%w: no choices", ErrUpstream)
	}
	// DeepSeek's JSON mode occasionally answers empty: a failure to answer.
	content := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if content == "" {
		return Extraction{}, fmt.Errorf("%w: empty content (finish_reason %q)", ErrUpstream, parsed.Choices[0].FinishReason)
	}
	return decodeExtraction(content)
}

// do sends req and returns the body of a 200. Errors carry the status only:
// an error body may quote the request, which is what the owner said.
func do(client *http.Client, req *http.Request, kind error) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", kind, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: read response: %v", kind, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", kind, resp.StatusCode)
	}
	return raw, nil
}

// decodeExtraction is lenient in shape (a number where a string was asked
// for is still the number) and strict in meaning (not an object, no answer).
func decodeExtraction(content string) (Extraction, error) {
	var fields map[string]any
	if err := json.Unmarshal([]byte(content), &fields); err != nil {
		return Extraction{}, fmt.Errorf("%w: content is not a JSON object", ErrUpstream)
	}
	text := func(key string) string {
		switch v := fields[key].(type) {
		case string:
			return strings.TrimSpace(v)
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		}
		return ""
	}
	list := func(key string) []string {
		items, _ := fields[key].([]any)
		var out []string
		for _, item := range items {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	}
	return Extraction{
		Title: text("title"), Kind: text("kind"), DurationMin: text("duration_min"),
		ProjectRef: text("project"), Project: text("project_heard"),
		CompanyRef: text("company"), Company: text("company_heard"),
		Date: text("date"), Start: text("start"), End: text("end"), DueDate: text("due_date"),
		Tags: list("tags"), NewTags: list("new_tags"),
	}, nil
}

// Transcriber calls an OpenAI-compatible /audio/transcriptions API.
type Transcriber struct {
	baseURL, apiKey, model string
	http                   *http.Client
}

// NewTranscriber builds a client for baseURL, e.g. https://api.openai.com/v1.
func NewTranscriber(baseURL, apiKey, model string, httpClient *http.Client) *Transcriber {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Transcriber{strings.TrimRight(baseURL, "/"), apiKey, model, httpClient}
}

// Audio is one recording. Filename carries the extension, which is how the
// API tells the format: "voice.m4a" from Safari, "voice.webm" from Chrome.
type Audio struct {
	Data     []byte
	Filename string
}

// Transcribe turns audio into text, prompted with the owner's names so they
// come back spelled as the owner spells them.
func (t *Transcriber) Transcribe(ctx context.Context, audio Audio, names []string) (string, error) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for k, v := range map[string]string{
		"model": t.model, "response_format": "json", "temperature": "0", "prompt": transcriptionPrompt(names),
	} {
		if v != "" {
			if err := form.WriteField(k, v); err != nil {
				return "", err
			}
		}
	}
	part, err := form.CreateFormFile("file", audio.Filename)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(audio.Data); err != nil {
		return "", err
	}
	if err := form.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/audio/transcriptions", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.apiKey)

	raw, err := do(t.http, req, ErrTranscription)
	if err != nil {
		return "", err
	}
	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("%w: decode response: %v", ErrTranscription, err)
	}
	return strings.TrimSpace(parsed.Text), nil
}

// maxPromptRunes: transcription models only read the start of a prompt.
const maxPromptRunes = 800

func transcriptionPrompt(names []string) string {
	var b strings.Builder
	b.WriteString("A task for a daily planner, in Spanish or English.")
	if len(names) == 0 {
		return b.String()
	}
	b.WriteString(" Names: ")
	for i, name := range names {
		next := name
		if i > 0 {
			next = ", " + name
		}
		if len([]rune(b.String()))+len([]rune(next)) > maxPromptRunes {
			break
		}
		b.WriteString(next)
	}
	b.WriteString(".")
	return b.String()
}
