// Command omnitodo runs the OmniTodo server.
//
//	omnitodo                  serve (default)
//	omnitodo seed             fill an empty database with sample data
//	omnitodo hash-passphrase  read a passphrase from stdin, print OMNITODO_PASSPHRASE_HASH
//	omnitodo backup <file>    write a consistent copy of the database to <file>
//	omnitodo healthcheck      probe the running server's /healthz (for the container
//	                          healthcheck: the distroless image has no shell or curl)
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // the configured timezone must resolve on any base image

	"github.com/ocalderonc/omnitodo/internal/auth"
	"github.com/ocalderonc/omnitodo/internal/civil"
	"github.com/ocalderonc/omnitodo/internal/server"
	"github.com/ocalderonc/omnitodo/internal/store"
	"github.com/ocalderonc/omnitodo/internal/voice"
)

type config struct {
	addr           string
	db             string
	passphraseHash string
	sessionSecret  string
	cookieSecure   bool
	noAuth         bool

	// Voice entry (internal/voice). Off until OMNITODO_LLM_API_KEY is set;
	// the microphone also needs OMNITODO_STT_API_KEY.
	llmBaseURL, llmAPIKey, llmModel string
	sttBaseURL, sttAPIKey, sttModel string
}

func loadConfig() config {
	return config{
		addr:           env("OMNITODO_ADDR", ":8080"),
		db:             env("OMNITODO_DB", "./data/omnitodo.db"),
		passphraseHash: os.Getenv("OMNITODO_PASSPHRASE_HASH"),
		sessionSecret:  os.Getenv("OMNITODO_SESSION_SECRET"),
		cookieSecure:   env("OMNITODO_COOKIE_SECURE", "true") != "false",
		// Deliberately awkward to set: without auth anyone who can reach the
		// port has the data. Only for local development (make run).
		noAuth: os.Getenv("OMNITODO_INSECURE_NO_AUTH") == "1",

		llmBaseURL: env("OMNITODO_LLM_BASE_URL", "https://api.deepseek.com"),
		llmAPIKey:  strings.TrimSpace(os.Getenv("OMNITODO_LLM_API_KEY")),
		llmModel:   env("OMNITODO_LLM_MODEL", "deepseek-flash"),
		sttBaseURL: env("OMNITODO_STT_BASE_URL", "https://api.openai.com/v1"),
		sttAPIKey:  strings.TrimSpace(os.Getenv("OMNITODO_STT_API_KEY")),
		sttModel:   env("OMNITODO_STT_MODEL", "gpt-4o-transcribe"),
	}
}

// voiceService builds voice entry from the config, or nil when it is off.
// A key is only ever sent over https (or to localhost, for testing).
func voiceService(cfg config) (*voice.Service, error) {
	if cfg.llmAPIKey == "" {
		if cfg.sttAPIKey != "" {
			return nil, errors.New("OMNITODO_STT_API_KEY needs OMNITODO_LLM_API_KEY too: the transcript is read by the language model")
		}
		return nil, nil
	}
	if err := checkAPIURL("OMNITODO_LLM_BASE_URL", cfg.llmBaseURL); err != nil {
		return nil, err
	}
	llm := voice.NewLLM(cfg.llmBaseURL, cfg.llmAPIKey, cfg.llmModel, nil)
	if cfg.sttAPIKey == "" {
		return voice.NewService(llm, nil), nil
	}
	if err := checkAPIURL("OMNITODO_STT_BASE_URL", cfg.sttBaseURL); err != nil {
		return nil, err
	}
	return voice.NewService(llm, voice.NewTranscriber(cfg.sttBaseURL, cfg.sttAPIKey, cfg.sttModel, nil)), nil
}

func checkAPIURL(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%s must be an absolute URL, such as https://api.deepseek.com", name)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1")) {
		return fmt.Errorf("%s must be https: the API key would travel in clear text", name)
	}
	return nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	cfg := loadConfig()
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd {
	case "serve":
		err = serve(ctx, cfg)
	case "seed":
		err = seed(ctx, cfg)
	case "healthcheck":
		err = healthcheck(cfg)
	case "hash-passphrase":
		err = hashPassphrase()
	case "backup":
		if len(os.Args) != 3 {
			err = errors.New("usage: omnitodo backup <file>")
			break
		}
		err = backup(ctx, cfg, os.Args[2])
	default:
		err = fmt.Errorf("unknown command %q (want serve, seed, backup, hash-passphrase or healthcheck)", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "omnitodo:", err)
		os.Exit(1)
	}
}

func seed(ctx context.Context, cfg config) error {
	st, err := store.Open(ctx, cfg.db)
	if err != nil {
		return err
	}
	defer st.Close()
	settings, err := st.GetSettings(ctx)
	if err != nil {
		return err
	}
	loc, err := settings.Location()
	if err != nil {
		return err
	}
	if err := st.Seed(ctx, civil.DateOf(time.Now().In(loc))); err != nil {
		return err
	}
	fmt.Println("seeded", cfg.db)
	return nil
}

func serve(ctx context.Context, cfg config) error {
	st, err := store.Open(ctx, cfg.db)
	if err != nil {
		return err
	}
	defer st.Close()

	scfg := server.Config{
		PassphraseHash: cfg.passphraseHash,
		CookieSecure:   cfg.cookieSecure,
		NoAuth:         cfg.noAuth,
	}
	if scfg.Voice, err = voiceService(cfg); err != nil {
		return err
	}
	switch {
	case scfg.Voice.Transcribes():
		slog.Info("voice entry on", "llm", cfg.llmModel, "stt", cfg.sttModel)
	case scfg.Voice != nil:
		slog.Info("voice entry: language model only, no microphone (set OMNITODO_STT_API_KEY)")
	}
	if cfg.noAuth {
		slog.Warn("OMNITODO_INSECURE_NO_AUTH=1: login is disabled, anyone who can reach this port can use the app")
	} else {
		if err := auth.ValidateHash(cfg.passphraseHash); err != nil {
			return fmt.Errorf("OMNITODO_PASSPHRASE_HASH: %w", err)
		}
		if scfg.Sessions, err = auth.NewSessions(cfg.sessionSecret, 365*24*time.Hour); err != nil {
			return fmt.Errorf("OMNITODO_SESSION_SECRET: %w", err)
		}
	}
	handler, err := server.New(st, scfg)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("listening", "addr", cfg.addr, "db", cfg.db)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func backup(ctx context.Context, cfg config, path string) error {
	st, err := store.Open(ctx, cfg.db)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Backup(ctx, path); err != nil {
		return err
	}
	fmt.Println("backed up", cfg.db, "to", path)
	return nil
}

func hashPassphrase() error {
	fmt.Fprintln(os.Stderr, "Passphrase (input is read from stdin, one line):")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return err
	}
	h, err := auth.HashPassphrase(strings.TrimRight(line, "\r\n"))
	if err != nil {
		return err
	}
	fmt.Println(h)
	return nil
}

func healthcheck(cfg config) error {
	_, port, err := net.SplitHostPort(cfg.addr)
	if err != nil {
		return err
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz: %s", resp.Status)
	}
	return nil
}
