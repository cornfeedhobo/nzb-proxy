// Package app wires the proxy service and manages its lifecycle.
package app

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

	"github.com/cornfeedhobo/nzb-proxy/internal/cache"
	"github.com/cornfeedhobo/nzb-proxy/internal/config"
	"github.com/cornfeedhobo/nzb-proxy/internal/proxy"
	"github.com/cornfeedhobo/nzb-proxy/internal/state"
)

// Run loads configuration and serves requests until shutdown or a fatal error.
func Run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.CacheDir, 0700); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}
	unlock, err := state.LockCache(filepath.Join(cfg.CacheDir, ".lock"))
	if err != nil {
		return err
	}
	defer unlock()
	signingKey, err := state.LoadSigningKey(cfg.CacheDir)
	if err != nil {
		return err
	}
	store, err := cache.New(cfg.CacheDir, cfg.CacheTTL)
	if err != nil {
		return err
	}
	handler, err := proxy.New(proxy.Config{
		UpstreamURL: cfg.UpstreamURL, PublicURL: cfg.PublicURL, APIKey: cfg.APIKey,
		SigningKey:   signingKey,
		AllowedHosts: cfg.AllowedHosts, FetchTimeout: cfg.FetchTimeout, MaxNZBBytes: cfg.MaxNZBBytes,
	}, store)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("/", handler)
	server := &http.Server{
		Addr: cfg.Listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: cfg.FetchTimeout + 30*time.Second,
		IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		prune := func() {
			if err := store.Prune(); err != nil {
				slog.Warn("cache cleanup failed", "error", err)
			}
		}
		prune()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				prune()
			}
		}
	}()
	defer func() { stop(); <-cleanupDone }()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	slog.Info("NZB proxy started", "listen", cfg.Listen, "cache_ttl", cfg.CacheTTL)
	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.FetchTimeout+5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return err
		}
	}
	return nil
}
