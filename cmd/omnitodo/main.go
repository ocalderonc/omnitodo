// Command omnitodo runs the OmniTodo server.
//
//	omnitodo              serve (default)
//	omnitodo seed         fill an empty database with sample data
//	omnitodo healthcheck  probe the running server's /healthz (for the container
//	                      healthcheck: the distroless image has no shell or curl)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ocalderonc/omnitodo/internal/civil"
	"github.com/ocalderonc/omnitodo/internal/store"
)

type config struct {
	addr string
	db   string
}

func loadConfig() config {
	return config{
		addr: env("OMNITODO_ADDR", ":8080"),
		db:   env("OMNITODO_DB", "./data/omnitodo.db"),
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
	default:
		err = fmt.Errorf("unknown command %q (want serve, seed or healthcheck)", cmd)
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

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})

	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
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
