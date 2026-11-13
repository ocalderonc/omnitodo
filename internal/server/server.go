// Package server is the HTTP layer: routing, auth, templates and handlers.
//
// Pages are server-rendered; HTMX requests get HTML fragments back. Every
// mutation of a day answers with the whole #day region re-rendered, so the
// list, timeline and summary can never disagree with each other.
package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ocalderonc/omnitodo/internal/auth"
	"github.com/ocalderonc/omnitodo/internal/schedule"
	"github.com/ocalderonc/omnitodo/internal/store"
	"github.com/ocalderonc/omnitodo/internal/voice"
	"github.com/ocalderonc/omnitodo/web"
)

const sessionCookie = "omnitodo_session"

type Config struct {
	// PassphraseHash and Sessions are required unless NoAuth is set.
	PassphraseHash string
	Sessions       *auth.Sessions
	NoAuth         bool
	CookieSecure   bool
	Now            func() time.Time // defaults to time.Now
	// Voice reads spoken activities; nil switches voice entry off.
	Voice  *voice.Service
	Logger *slog.Logger
}

type Server struct {
	store   *store.Store
	cfg     Config
	log     *slog.Logger
	limiter *auth.Limiter
	pages   map[string]*template.Template
	frags   *template.Template
	assetV  string // content hash of the static files, for cache busting
	static  fs.FS
	sw      []byte // generated service worker
}

func New(st *store.Store, cfg Config) (http.Handler, error) {
	if !cfg.NoAuth && (cfg.Sessions == nil || auth.ValidateHash(cfg.PassphraseHash) != nil) {
		return nil, errors.New("auth: a valid passphrase hash and session secret are required (or set NoAuth)")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	static, err := fs.Sub(web.Static, "static")
	if err != nil {
		return nil, err
	}
	s := &Server{
		store:   st,
		cfg:     cfg,
		log:     cfg.Logger,
		limiter: auth.NewLimiter(10, time.Minute),
		static:  static,
	}
	if s.assetV, err = hashFS(static); err != nil {
		return nil, err
	}
	if s.sw, err = s.buildServiceWorker(); err != nil {
		return nil, err
	}
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	return s.routes(), nil
}

func (s *Server) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(s.recoverer, securityHeaders, sameOrigin)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	r.Handle("/static/*", s.staticHandler())
	r.Get("/manifest.webmanifest", s.serveManifest)
	r.Get("/sw.js", s.serveServiceWorker)
	r.Get("/login", s.loginPage)
	r.Post("/login", s.login)
	r.Post("/logout", s.logout)

	r.Group(func(r chi.Router) {
		r.Use(s.requireAuth)
		r.Get("/", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/day", http.StatusSeeOther) })
		r.Get("/day", s.dayPage)
		r.Get("/day/{date}", s.dayPage)
		r.Get("/api/day/{date}", s.dayJSON)

		r.Post("/activities", s.createActivity)
		r.Get("/activities/new", s.newForm)
		r.Get("/activities/{id}/edit", s.editForm)
		r.Patch("/activities/{id}", s.updateActivity)
		r.Post("/activities/{id}/toggle", s.toggleActivity)
		r.Post("/activities/{id}/slot", s.slotActivity)
		r.Delete("/activities/{id}", s.deleteActivity)
		r.Post("/voice", s.voiceDraft)

		r.Get("/settings", s.settingsPage)
		r.Post("/settings/companies", s.createCompany)
		r.Post("/settings/companies/{id}", s.updateCompany)
		r.Post("/settings/companies/{id}/delete", s.deleteCompany)
		r.Post("/settings/projects", s.createProject)
		r.Post("/settings/projects/{id}", s.updateProject)
		r.Post("/settings/projects/{id}/delete", s.deleteProject)
		r.Post("/settings/tags/{id}", s.updateTag)
		r.Post("/settings/tags/{id}/delete", s.deleteTag)
		r.Post("/settings/day", s.updateDaySettings)
	})
	return r
}

// --- templates ----------------------------------------------------------------

func (s *Server) funcs() template.FuncMap {
	return template.FuncMap{
		"dur":   schedule.FormatDuration,
		"nav":   func(current string, auth bool) navView { return navView{Current: current, Auth: auth} },
		"asset": func(name string) string { return "/static/" + name + "?v=" + s.assetV },
	}
}

func (s *Server) parseTemplates() error {
	base, err := template.New("").Funcs(s.funcs()).ParseFS(web.Templates, "templates/layout.html", "templates/form.html")
	if err != nil {
		return err
	}
	s.pages = map[string]*template.Template{}
	for _, name := range []string{"login", "day", "settings"} {
		t, err := template.Must(base.Clone()).ParseFS(web.Templates, "templates/"+name+".html")
		if err != nil {
			return err
		}
		s.pages[name] = t
	}
	// Fragments ("day", "form") all live in the day page's set.
	s.frags = s.pages["day"]
	return nil
}

type navView struct {
	Current string
	Auth    bool
}

type page struct {
	Title string
	Body  any
}

// render executes into a buffer first, so a template error becomes a clean
// 500 instead of half a page.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, t *template.Template, name string, data any) {
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, status int, name, title string, body any) {
	s.render(w, r, status, s.pages[name], "layout", page{Title: title, Body: body})
}

func (s *Server) renderFragment(w http.ResponseWriter, r *http.Request, name string, data any) {
	s.render(w, r, http.StatusOK, s.frags, name, data)
}

// --- errors -------------------------------------------------------------------

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	http.Error(w, "Something went wrong.", http.StatusInternalServerError)
}

// fail maps store errors to HTTP statuses.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "Not found.", http.StatusNotFound)
	case errors.Is(err, store.ErrInvalid), errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrAmbiguous):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		s.serverError(w, r, err)
	}
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("panic", "method", r.Method, "path", r.URL.Path, "panic", v)
				http.Error(w, "Something went wrong.", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// --- security -----------------------------------------------------------------

// The timeline positions blocks with inline style="--s:…" custom properties,
// hence style-src-attr. Scripts stay strictly 'self': no inline script, no
// eval (htmx's allowEval is off too).
const csp = "default-src 'self'; script-src 'self'; style-src 'self'; style-src-attr 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self'; connect-src 'self'; manifest-src 'self'; worker-src 'self'; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		// Not no-referrer: under it browsers send "Origin: null" on form posts.
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(self), geolocation=(), payment=(), usb=()")
		next.ServeHTTP(w, r)
	})
}

// sameOrigin rejects cross-site state-changing requests. The session cookie is
// SameSite=Lax, which already keeps it off cross-site POSTs; this is the second
// layer, using the headers every current browser sends.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		// Sec-Fetch-Site is set by the browser and cannot be forged by a page,
		// so when it is present it decides on its own. Origin is only the
		// fallback for browsers without it: browsers may send "Origin: null"
		// for legitimate same-origin form posts (e.g. under some referrer
		// policies), so it must not overrule a same-origin Sec-Fetch-Site.
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
			if site != "same-origin" && site != "none" {
				http.Error(w, "Cross-site request refused.", http.StatusForbidden)
				return
			}
		} else if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host {
				http.Error(w, "Cross-origin request refused.", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// --- static -------------------------------------------------------------------

func hashFS(fsys fs.FS) (string, error) {
	h := sha256.New()
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		h.Write([]byte(p))
		h.Write(b)
		return err
	})
	return hex.EncodeToString(h.Sum(nil))[:12], err
}

// Asset URLs carry ?v=<content hash>, so a matching request can be cached
// forever; anything else is revalidated.
func (s *Server) staticHandler() http.Handler {
	files := http.StripPrefix("/static/", http.FileServerFS(s.static))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if path.Ext(r.URL.Path) == "" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("v") == s.assetV {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

func isHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }
