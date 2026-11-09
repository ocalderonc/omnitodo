package server

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ocalderonc/omnitodo/internal/auth"
)

type loginView struct {
	Next  string
	Error string
}

func (s *Server) authenticated(r *http.Request) bool {
	if s.cfg.NoAuth {
		return true
	}
	c, err := r.Cookie(sessionCookie)
	return err == nil && s.cfg.Sessions.Valid(c.Value, s.cfg.Now())
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authenticated(r) {
			next.ServeHTTP(w, r)
			return
		}
		if isHTMX(r) {
			// A fragment swap cannot show a login page; make HTMX navigate.
			w.Header().Set("HX-Redirect", "/login")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "Log in first.", http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
	})
}

// safeNext only allows local paths, so ?next= cannot bounce to another site.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/day"
	}
	return next
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if s.authenticated(r) {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	s.renderPage(w, r, http.StatusOK, "login", "Log in", loginView{Next: r.URL.Query().Get("next")})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.cfg.NoAuth {
		http.Redirect(w, r, "/day", http.StatusSeeOther)
		return
	}
	next := r.PostFormValue("next")
	now := s.cfg.Now()
	if !s.limiter.Allow(now) {
		s.renderPage(w, r, http.StatusTooManyRequests, "login", "Log in",
			loginView{Next: next, Error: "Too many attempts. Wait a minute and try again."})
		return
	}
	if !auth.VerifyPassphrase(s.cfg.PassphraseHash, r.PostFormValue("passphrase")) {
		s.limiter.Fail(now)
		s.log.Warn("failed login", "remote", r.RemoteAddr, "forwarded_for", r.Header.Get("X-Forwarded-For"))
		s.renderPage(w, r, http.StatusUnauthorized, "login", "Log in", loginView{Next: next, Error: "Wrong passphrase."})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    s.cfg.Sessions.Issue(now),
		Path:     "/",
		MaxAge:   int(s.cfg.Sessions.TTL() / time.Second),
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		// Lax, not Strict: opening the installed PWA or a link to it from
		// another app is a top-level cross-site navigation, and Strict would
		// arrive logged out.
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, safeNext(next), http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
