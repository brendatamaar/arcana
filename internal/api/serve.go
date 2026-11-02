package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Serve owns the HTTP server until cancellation, then drains requests within timeout.
func Serve(ctx context.Context, listener net.Listener, handler http.Handler, timeout time.Duration, logger *slog.Logger) error {
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384, BaseContext: func(net.Listener) context.Context { return ctx }, ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError)}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serving API: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		drainer, managed := handler.(interface {
			beginShutdown()
			wait(context.Context) error
		})
		if managed {
			drainer.beginShutdown()
		}
		err := server.Shutdown(shutdownCtx)
		if err == nil && managed {
			err = drainer.wait(shutdownCtx)
		}
		if err != nil {
			_ = server.Close()
		}
		serveErr := <-done
		if err != nil {
			return fmt.Errorf("shutting down API: %w", err)
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			return fmt.Errorf("serving API: %w", serveErr)
		}
		return nil
	}
}
