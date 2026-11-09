// Command omnitodo runs the OmniTodo server.
//
//	omnitodo                  serve (default)
//	omnitodo seed             fill an empty database with sample data
//	omnitodo hash-passphrase  read a passphrase from stdin, print OMNITODO_PASSPHRASE_HASH
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
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ocalderonc/omnitodo/internal/auth"
	"github.com/ocalderonc/omnitodo/internal/civil"
	"github.com/ocalderonc/omnitodo/internal/server"
	"github.com/ocalderonc/omnitodo/internal/store"
)

type config struct {
	addr           string
	db             string
	passphraseHash string
	sessionSecret  string
	cookieSecure   bool
	noAuth         bool
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
	}
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
	default:
		err = fmt.Errorf("unknown command %q (want serve, seed, hash-passphrase or healthcheck)", cmd)
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
