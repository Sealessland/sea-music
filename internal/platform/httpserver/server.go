package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/sealessland/sea-music/internal/platform/config"
)

// New constructs an HTTP server using the configured address and timeouts, with handler serving all requests.
func New(cfg config.HTTP, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              cfg.Address,
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}
}

// Run serves until the context is cancelled, then performs a bounded graceful shutdown.
func Run(ctx context.Context, server *http.Server, shutdownTimeout time.Duration, logger *slog.Logger) error {
	result := make(chan error, 1)
	go func() {
		logger.Info("api listening", "address", server.Addr)
		result <- server.ListenAndServe()
	}()

	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		err := <-result
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
