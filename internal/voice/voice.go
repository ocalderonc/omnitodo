// Package voice turns a sentence someone said into a draft activity, the way
// Moneta's voice entry turns one into a movement.
//
// The phone records the audio; a transcription model (OpenAI by default)
// turns it into text, told the owner's company, project and tag names so it
// spells them as the owner does; a language model (DeepSeek by default) reads
// the text into fields. Both are OpenAI-compatible APIs.
//
// The rules:
//
//   - **It fills the form; it never saves.** The draft opens in the New
//     activity dialog, where the owner reads it and taps Save.
//   - **The model picks; the server checks.** The model is sent company,
//     project and tag names under short references ("c2", "p5") and answers
//     with references. Only references from that list become IDs; a name it
//     heard but could not place is matched by name, and otherwise left empty
//     and reported as unsure rather than guessed.
//   - **Names only.** Nothing else about the owner's data is sent: no
//     activities, no history. The audio and transcript are sent to the two
//     providers and nowhere else: never logged, never stored.
package voice

import (
	"errors"
	"strings"

	"github.com/ocalderonc/omnitodo/internal/civil"
)

var (
	// ErrDisabled means no language model is configured.
	ErrDisabled = errors.New("voice: not configured")
	// ErrBusy means too many requests in the last hour.
	ErrBusy = errors.New("voice: too many requests, try again later")
	// ErrUpstream means the language model did not answer usefully.
	ErrUpstream = errors.New("voice: the language model did not answer")
	// ErrTranscription means the transcription model did not answer usefully.
	ErrTranscription = errors.New("voice: the transcription model did not answer")
	// ErrInput means the request itself was unusable: empty, too long.
	ErrInput = errors.New("voice: invalid input")
)

// Vocabulary is everything the model is told about the owner: names.
type Vocabulary struct {
	Companies []Named
	Projects  []Named
	Tags      []string
}

// Named is one company or project as the model sees it.
type Named struct {
	// Ref is what the model answers with: "c1", "p7". Short, so it is copied
	// rather than mangled.
	Ref  string
	ID   int64
	Name string
	// CompanyRef and Company are a project's company, "" for none.
	CompanyRef string
	Company    string
}

// Names is every name, for the transcription prompt.
func (v Vocabulary) Names() []string {
	var names []string
	for _, c := range v.Companies {
		names = append(names, c.Name)
	}
	for _, p := range v.Projects {
		names = append(names, p.Name)
	}
	return append(names, v.Tags...)
}

func find(list []Named, ref string) *Named {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if ref == "" {
		return nil
	}
	for i := range list {
		if list[i].Ref == ref {
			return &list[i]
		}
	}
	return nil
}

// byName finds names equal ignoring case, as the store's name_key does.
func byName(list []Named, name string) []Named {
	key := strings.ToLower(strings.Join(strings.Fields(name), " "))
	var out []Named
	for _, n := range list {
		if key != "" && strings.ToLower(n.Name) == key {
			out = append(out, n)
		}
	}
	return out
}

// Extraction is what the model answered, before any of it is trusted.
type Extraction struct {
	Title       string
	Kind        string
	DurationMin string
	ProjectRef  string
	Project     string // as heard
	CompanyRef  string
	Company     string // as heard
	Date        string
	Start       string
	End         string
	DueDate     string
	Tags        []string
	NewTags     []string
}

// Draft is the extraction validated and resolved: what the form is filled
// with. Zero values mean not heard.
type Draft struct {
	Title       string
	Kind        string // "task" or "distractor"
	DurationMin int    // 0 = not heard
	Date        *civil.Date
	StartAt     *civil.Time
	DueDate     *civil.Date
	ProjectID   *int64
	CompanyID   *int64
	Tags        []string
	// Notes are for the owner to read before saving: what was unsure, what
	// was worked out ("start 15:40, to end at 16:00").
	Notes []string
}

// Heard is a recording transcribed and read.
type Heard struct {
	Transcript string
	Draft      Draft
}
