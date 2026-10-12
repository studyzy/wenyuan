package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
)

// Entrypoint is the application import target. A process manager resolves this
// symbol and receives the same served application that answers the public
// routes.
type Entrypoint struct{}

// Resolve composes the served application from the live environment. It is the
// single call a process manager makes at startup.
func (Entrypoint) Resolve(ctx context.Context) (*App, error) {
	if err := config.LoadDotenvFile(".env"); err != nil {
		return nil, err
	}
	cfg := config.FromEnvironment()
	return New(ctx, cfg, Dependencies{})
}

// Application aliases the composition entry so the import target reads as a
// single, stable symbol.
var Application = Entrypoint{}

// ServeOptions controls how the composed application is served.
type ServeOptions struct {
	// Listen overrides address binding, primarily for tests.
	Listen func(network, address string) (net.Listener, error)
}

// Serve composes the application and serves it until the context is cancelled.
// The listener is bound once so the probe and the serving socket use the same
// resolved address.
func Serve(ctx context.Context, cfg config.Config, deps Dependencies, opts ServeOptions) error {
	listen := opts.Listen
	if listen == nil {
		listen = net.Listen
	}
	listener, err := listen("tcp", cfg.Address())
	if err != nil {
		return err
	}

	composed, err := New(ctx, cfg, deps)
	if err != nil {
		listener.Close()
		return err
	}

	server := &http.Server{
		Handler:           composed.Handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-shutdownDone
	return nil
}
