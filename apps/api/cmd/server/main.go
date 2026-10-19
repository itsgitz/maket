// Command server runs maket: the JSON API, the embedded dashboard, and
// preview serving, all routed by Host header.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/itsgitz/maket/apps/api/internal/auth"
	"github.com/itsgitz/maket/apps/api/internal/config"
	"github.com/itsgitz/maket/apps/api/internal/db"
	"github.com/itsgitz/maket/apps/api/internal/httpapi"
	"github.com/itsgitz/maket/apps/api/internal/oauth/github"
	"github.com/itsgitz/maket/apps/api/internal/preview"
	"github.com/itsgitz/maket/apps/api/internal/storage"
	"github.com/itsgitz/maket/apps/api/internal/webui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "maket: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	database, err := db.Open(ctx, cfg.DBPath())
	if err != nil {
		return err
	}
	defer database.Close()

	store, err := storage.NewLocal(cfg.StorageDir())
	if err != nil {
		return err
	}
	defer store.Close()

	allow, err := auth.NewAllowlist(cfg.AllowedGitHubUsers)
	if err != nil {
		return err
	}
	authSvc := auth.NewService(database, allow, time.Now)
	go cleanup(ctx, log, authSvc)

	srv := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: httpapi.New(cfg, httpapi.Deps{
			Dashboard: webui.Handler(),
			Previews:  preview.Handler{},
			Auth:      authSvc,
			GitHub: github.New(github.Config{
				ClientID:     cfg.GitHubClientID,
				ClientSecret: cfg.GitHubClientSecret,
				RedirectURL:  cfg.GitHubRedirectURL(),
			}),
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.ListenAddr, "app", cfg.AppOrigin(), "previews", cfg.PreviewURL("<slug>"))
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// cleanup runs the housekeeping jobs at startup and then hourly until ctx
// ends.
func cleanup(ctx context.Context, log *slog.Logger, authSvc *auth.Service) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		if n, err := authSvc.DeleteExpiredSessions(ctx); err != nil {
			log.Error("cleanup: expired sessions", "err", err)
		} else if n > 0 {
			log.Info("cleanup: deleted expired sessions", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
