package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/monitoring"
)

// Entrypoint is the application import target. A process manager resolves this
// symbol and receives the same served application that answers the public
// routes.
type Entrypoint struct{}

// Resolve composes the served application from the live environment. It is the
// single call a process manager makes at startup.
func (Entrypoint) Resolve(ctx context.Context) (*App, error) {
	if err := config.LoadDotenvForEntrypoint(); err != nil {
		return nil, err
	}
	cfg := config.FromEnvironment()
	return New(ctx, cfg, Dependencies{})
}

// Application aliases the composition entry so the import target reads as a
// single, stable symbol.
var Application = Entrypoint{}

// bindListener returns the socket this process serves on: the one a supervising
// parent passed it when this process is a worker, the caller's own factory when
// one was supplied, and otherwise a freshly bound address.
func bindListener(cfg config.Config, opts ServeOptions) (net.Listener, error) {
	if opts.Listen != nil {
		return opts.Listen("tcp", cfg.Address())
	}
	listener, inherited, err := inheritedListener()
	if err != nil {
		return nil, err
	}
	if inherited {
		return listener, nil
	}
	return net.Listen("tcp", cfg.Address())
}

// ServeOptions controls how the composed application is served.
type ServeOptions struct {
	// Listen overrides address binding, primarily for tests.
	Listen func(network, address string) (net.Listener, error)
	// TLSConfig serves HTTPS when a certificate was configured. A nil value
	// serves plaintext, which is the reference's behaviour with no --ssl-* flag.
	TLSConfig *tls.Config
}

// Serve composes the application and serves it until the context is cancelled.
// The listener is bound once so the probe and the serving socket use the same
// resolved address.
func Serve(ctx context.Context, cfg config.Config, deps Dependencies, opts ServeOptions) error {
	listener, err := bindListener(cfg, opts)
	if err != nil {
		return err
	}

	// The address is held from here on, so composition must not probe it.
	deps.AddressAlreadyBound = true
	composed, err := New(ctx, cfg, deps)
	if err != nil {
		_ = listener.Close()
		return err
	}

	// The management plane is a second listener in the same process. It is
	// bound after composition so an admin-schema migration failure is reported
	// before the port is taken, and it is shut down with the data plane.
	var adminServer *http.Server
	if composed.AdminHandler != nil {
		adminListener, err := net.Listen("tcp", cfg.AdminListen)
		if err != nil {
			_ = listener.Close()
			return fmt.Errorf("bind admin listener %q: %w", cfg.AdminListen, err)
		}
		adminServer = &http.Server{
			Handler:           composed.AdminHandler,
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() {
			if err := adminServer.Serve(adminListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("admin server stopped", "error", err)
			}
		}()
	}

	// The reference's standalone mode drains the queue and runs housekeeping in
	// the same process as the API, so a deployment that runs only the server
	// still processes its queued work. The roles share the served application's
	// store and provider and stop with the context.
	bgCtx, bgCancel := context.WithCancel(ctx)
	stopBackground := startBackgroundRoles(bgCtx, composed)
	startWorkerMetrics(bgCtx, cfg, composed)
	defer func() {
		bgCancel()
		stopBackground()
		// The servers have stopped by the time Serve returns, so the tracer's
		// queued spans can be flushed with no completion still producing one.
		composed.Close()
	}()

	// The stall probe reports a scheduler stall, which a liveness probe
	// would otherwise only observe as a timeout. It runs when the reference's
	// watchdog is enabled, and its cadence is the resolved watchdog interval.
	// The probe runs when the watchdog is on, and also when the lag histogram is
	// requested on its own: the reference's metric is fed by the same probe, so
	// asking for the series is asking for the sampling.
	if (cfg.MetricsTuning.LoopWatchdog || cfg.MetricsTuning.LoopLagMetric) && cfg.LoopWatchdogInterval > 0 {
		probe := &monitoring.LoopLagProbe{
			Interval: cfg.LoopWatchdogInterval,
			Stall:    cfg.LoopWatchdogStall,
			Logger:   slog.Default(),
		}
		go probe.Run(bgCtx)
	}

	server := &http.Server{
		Handler:           composed.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig:         opts.TLSConfig,
	}

	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		if adminServer != nil {
			_ = adminServer.Shutdown(shutdownCtx)
		}
	}()

	// The certificate, when there is one, is already in the server's
	// configuration, so ServeTLS only has to switch the handshake on.
	var serveErr error
	if opts.TLSConfig != nil {
		serveErr = server.ServeTLS(listener, "", "")
	} else {
		serveErr = server.Serve(listener)
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	<-shutdownDone
	return nil
}
