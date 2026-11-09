package server

import (
	"bytes"
	"io/fs"
	"net/http"
	"text/template"
)

// The manifest and service worker are public (the browser fetches the
// manifest without cookies) and live at the root so the worker's scope is the
// whole app.

const manifest = `{
  "id": "/",
  "name": "OmniTodo",
  "short_name": "OmniTodo",
  "description": "A to-do list on steroids: tasks, distractors and a daily timeline.",
  "start_url": "/day",
  "scope": "/",
  "display": "standalone",
  "background_color": "#f7f7f5",
  "theme_color": "#4f46e5",
  "icons": [
    {"src": "/static/icons/icon-192.png", "sizes": "192x192", "type": "image/png"},
    {"src": "/static/icons/icon-512.png", "sizes": "512x512", "type": "image/png"},
    {"src": "/static/icons/maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"}
  ]
}
`

// Static assets: cache-first, precached per build (the cache name carries the
// asset hash, so a deploy replaces it). Day pages: network-first, and the last
// copy of each is kept so the app opens offline. HTMX requests and every
// non-GET go straight to the network: writes need the server (README §1).
var swTemplate = template.Must(template.New("sw").Parse(`// Generated per build. Do not edit: see internal/server/pwa.go.
const STATIC = "static-{{.Version}}";
const PAGES = "pages";
const PRECACHE = [{{range $i, $a := .Assets}}{{if $i}}, {{end}}"{{$a}}"{{end}}];

self.addEventListener("install", (e) => {
  e.waitUntil(caches.open(STATIC).then((c) => c.addAll(PRECACHE)).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (e) => {
  e.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k.startsWith("static-") && k !== STATIC).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

const offline = () => new Response(
  "<!doctype html><meta charset=utf-8><meta name=viewport content='width=device-width'>" +
  "<title>Offline</title><p style='font:16px system-ui;padding:2rem'>You are offline and this day has not been opened on this device yet.</p>",
  { status: 503, headers: { "Content-Type": "text/html; charset=utf-8" } },
);

self.addEventListener("fetch", (e) => {
  const req = e.request;
  const url = new URL(req.url);
  if (req.method !== "GET" || url.origin !== self.location.origin || req.headers.get("HX-Request")) return;

  if (url.pathname.startsWith("/static/")) {
    e.respondWith(caches.match(req).then((hit) => hit || fetch(req)));
    return;
  }
  if (req.mode === "navigate") {
    e.respondWith(
      fetch(req)
        .then((res) => {
          if (res.ok && !res.redirected && url.pathname.startsWith("/day")) {
            const copy = res.clone();
            caches.open(PAGES).then((c) => c.put(req, copy));
          }
          return res;
        })
        .catch(() => caches.match(req, { cacheName: PAGES }).then((hit) => hit || offline())),
    );
  }
});
`))

func (s *Server) serveManifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(manifest))
}

func (s *Server) buildServiceWorker() ([]byte, error) {
	var assets []string
	err := fs.WalkDir(s.static, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		// Precache with the same ?v= URLs the pages reference.
		assets = append(assets, "/static/"+p+"?v="+s.assetV)
		return nil
	})
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	err = swTemplate.Execute(&buf, struct {
		Version string
		Assets  []string
	}{s.assetV, assets})
	return buf.Bytes(), err
}

func (s *Server) serveServiceWorker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	// Browsers cap worker caching at 24h anyway; no-cache makes a deploy
	// reach installed apps on their next launch.
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(s.sw)
}
