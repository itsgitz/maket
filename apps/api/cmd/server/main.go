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
	"path/filepath"
	"syscall"
	"time"

	"github.com/itsgitz/maket/apps/api/internal/auth"
	"github.com/itsgitz/maket/apps/api/internal/cfpurge"
	"github.com/itsgitz/maket/apps/api/internal/config"
	"github.com/itsgitz/maket/apps/api/internal/db"
	"github.com/itsgitz/maket/apps/api/internal/httpapi"
	"github.com/itsgitz/maket/apps/api/internal/oauth/github"
	"github.com/itsgitz/maket/apps/api/internal/preview"
	"github.com/itsgitz/maket/apps/api/internal/prototype"
	"github.com/itsgitz/maket/apps/api/internal/prototype/archive"
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

	store, closeStore, err := openStorage(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer closeStore()

	allow, err := auth.NewAllowlist(cfg.AllowedGitHubUsers)
	if err != nil {
		return err
	}
	authSvc := auth.NewService(database, allow, time.Now)

	tmpDir := filepath.Join(cfg.DataDir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return fmt.Errorf("create tmp dir: %w", err)
	}
	var purger prototype.Purger // stays a nil interface when purging is off
	if cfg.CloudflarePurge() {
		purger = cfpurge.New(cfg.CloudflareAPIToken, cfg.CloudflareZoneID)
	}
	protoSvc := prototype.NewService(database, store, allow, prototype.Config{
		Limits:      archive.Limits{MaxFiles: cfg.MaxFiles, MaxExtractedBytes: cfg.MaxExtractedBytes},
		QuotaBytes:  cfg.UserQuotaBytes,
		TmpDir:      tmpDir,
		PreviewHost: func(slug string) string { return slug + "." + cfg.PreviewDomain },
	}, purger, time.Now)

	go cleanup(ctx, log, authSvc, protoSvc)

	srv := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: httpapi.New(cfg, httpapi.Deps{
			Dashboard: webui.Handler(),
			Previews:  &preview.Handler{Lookup: protoSvc, Store: store},
			Auth:      authSvc,
			GitHub: github.New(github.Config{
				ClientID:     cfg.GitHubClientID,
				ClientSecret: cfg.GitHubClientSecret,
				RedirectURL:  cfg.GitHubRedirectURL(),
			}),
			Prototypes:     protoSvc,
			MaxUploadBytes: cfg.MaxUploadBytes,
			TmpDir:         tmpDir,
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

// openStorage builds the Prototype file store named by MAKET_STORAGE_DRIVER.
// For s3 it checks the bucket is reachable, so a wrong name or credentials
// stop the server at startup. The returned func releases the store and is
// safe to call once.
func openStorage(ctx context.Context, cfg config.Config, log *slog.Logger) (storage.Storage, func(), error) {
	if cfg.StorageDriver == "s3" {
		s, err := storage.NewS3(storage.S3Config{
			Endpoint:        cfg.S3Endpoint,
			Bucket:          cfg.S3Bucket,
			Region:          cfg.S3Region,
			AccessKeyID:     cfg.S3AccessKeyID,
			SecretAccessKey: cfg.S3SecretAccessKey,
		})
		if err != nil {
			return nil, nil, err
		}
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := s.Check(checkCtx); err != nil {
			return nil, nil, err
		}
		log.Info("storage", "driver", "s3", "bucket", cfg.S3Bucket)
		return s, func() {}, nil
	}
	local, err := storage.NewLocal(cfg.StorageDir())
	if err != nil {
		return nil, nil, err
	}
	log.Info("storage", "driver", "local", "dir", cfg.StorageDir())
	return local, func() { local.Close() }, nil
}

// cleanup runs the housekeeping jobs at startup and then hourly until ctx
// ends.
func cleanup(ctx context.Context, log *slog.Logger, authSvc *auth.Service, protoSvc *prototype.Service) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		if n, err := authSvc.DeleteExpiredSessions(ctx); err != nil {
			log.Error("cleanup: expired sessions", "err", err)
		} else if n > 0 {
			log.Info("cleanup: deleted expired sessions", "count", n)
		}
		if err := protoSvc.Cleanup(ctx, time.Hour); err != nil {
			log.Error("cleanup: prototype leftovers", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
