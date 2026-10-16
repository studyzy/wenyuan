// Command wenyuan is the process entrypoint for the agent-memory HTTP
// API. It resolves configuration from the environment, composes the served
// application, and runs the HTTP server.
//
// The process shape is the reference's: --daemon detaches the process into the
// background, --workers N runs N copies behind one listening socket, and
// --proxy-headers makes the forwarded client information trusted when the peer
// is in the allow list.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/studyzy/wenyuan/internal/app"
	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/monitoring"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet(config.EntrypointName, flag.ContinueOnError)
	// --access-log and --no-access-log share one destination, so the last one on
	// the command line decides, which is how the reference's parser resolves a
	// launcher that passes both.
	accessLog := &accessLogSwitch{}
	var (
		host             = flags.String("host", "", "bind host (overrides the environment and the default)")
		port             = flags.Int("port", 0, "bind port (overrides the environment and the default)")
		logLevel         = flags.String("log-level", "", "log level")
		workers          = flags.Int("workers", 0, "number of worker processes")
		proxyHeaders     = flags.Bool("proxy-headers", false, "honour forwarded proxy headers")
		forwardedAllowIP = flags.String("forwarded-allow-ips", "", "comma separated list of IPs trusted with proxy headers")
		reload           = flags.Bool("reload", false, "reload on code changes (development only)")
		idleTimeout      = flags.Int("idle-timeout", 0, "retired daemon idle exit, accepted for compatibility")
		sslKeyFile       = flags.String("ssl-keyfile", "", "TLS private key file")
		sslCertFile      = flags.String("ssl-certfile", "", "TLS certificate file")
		daemon           = flags.Bool("daemon", false, "run as a background daemon")
	)
	flags.Var(&accessLogFlag{target: accessLog}, "access-log", "emit an access log line per request")
	flags.Var(&accessLogFlag{target: accessLog, negate: true}, "no-access-log", "disable the access log")

	if err := flags.Parse(args); err != nil {
		return err
	}

	if err := config.LoadDotenvForEntrypoint(); err != nil {
		return err
	}

	// The daemon's detached child is told by the environment rather than by the
	// flag (the flag is stripped so it does not detach again), so both spellings
	// resolve the daemon's host and port the same way.
	daemonRequested := optionalBool(flags, "daemon", daemon)
	if app.IsDaemonChild() {
		requested := true
		daemonRequested = &requested
	}

	base := config.FromEnvironment()
	cfg := config.ApplyFlags(base, config.FlagValues{
		Host:              optionalString(flags, "host", host),
		Port:              optionalInt(flags, "port", port),
		LogLevel:          optionalString(flags, "log-level", logLevel),
		Workers:           optionalInt(flags, "workers", workers),
		AccessLog:         accessLog.resolved(),
		ProxyHeaders:      optionalBool(flags, "proxy-headers", proxyHeaders),
		ForwardedAllowIPs: optionalString(flags, "forwarded-allow-ips", forwardedAllowIP),
		Reload:            optionalBool(flags, "reload", reload),
		IdleTimeout:       optionalInt(flags, "idle-timeout", idleTimeout),
		SSLKeyFile:        optionalString(flags, "ssl-keyfile", sslKeyFile),
		SSLCertFile:       optionalString(flags, "ssl-certfile", sslCertFile),
		Daemon:            daemonRequested,
	})

	// The retired idle exit is accepted so an existing launcher still starts; it
	// is inert in the reference too, and says so on the same stream.
	if cfg.IdleTimeout != 0 {
		fmt.Fprintf(os.Stderr, "--idle-timeout %d is ignored: the daemon no longer auto-exits when idle.\n", cfg.IdleTimeout)
	}
	if cfg.Reload {
		fmt.Fprintln(os.Stderr, "--reload is not supported by this build: a compiled binary reloads nothing, so restart it to pick up changes.")
	}

	if detached, err := app.Daemonize(cfg); err != nil || detached {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	slog.SetDefault(slog.New(monitoring.NewLogHandler(cfg)))
	defer stop()

	// A supervised worker serves; the supervisor that spawned it does not.
	if cfg.Workers > 1 && !app.IsWorkerChild() {
		slog.Info("supervising workers", "workers", cfg.Workers, "address", cfg.Address(), "entrypoint", config.EntrypointName)
		return app.Supervise(ctx, cfg, app.DaemonArguments(args))
	}

	slog.Info("starting", "address", cfg.Address(), "entrypoint", config.EntrypointName)
	if path := config.LoadedConfigPath(); path != "" {
		slog.Info("read configuration file", "path", path)
	}

	tlsConfig, err := serveTLS(cfg)
	if err != nil {
		return err
	}
	return app.Serve(ctx, cfg, app.Dependencies{}, app.ServeOptions{TLSConfig: tlsConfig})
}

// serveTLS builds the TLS configuration when a certificate was configured. The
// reference serves HTTPS when both a key and a certificate are given, and a
// solo value is a configuration error rather than a silently plaintext server.
func serveTLS(cfg config.Config) (*tls.Config, error) {
	key, cert := cfg.SSLKeyFile, cfg.SSLCertFile
	if key == "" && cert == "" {
		return nil, nil
	}
	if key == "" || cert == "" {
		return nil, fmt.Errorf("--ssl-keyfile and --ssl-certfile must be given together")
	}
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}, nil
}

// accessLogSwitch is the shared destination of --access-log and
// --no-access-log: the last flag on the command line writes it.
type accessLogSwitch struct {
	value bool
	set   bool
}

// resolved returns the switch the launcher passed, or nil when it passed none,
// so the environment and the default still apply.
func (s *accessLogSwitch) resolved() *bool {
	if !s.set {
		return nil
	}
	value := s.value
	return &value
}

// accessLogFlag is one of the two access-log flags, writing into the shared
// switch.
type accessLogFlag struct {
	target *accessLogSwitch
	negate bool
}

// String renders the flag's current value for the flag package's usage output.
func (f *accessLogFlag) String() string { return "false" }

// Set records one occurrence of the flag.
func (f *accessLogFlag) Set(raw string) error {
	value, err := parseBoolValue(raw, !f.negate)
	if err != nil {
		return err
	}
	f.target.value = value
	f.target.set = true
	return nil
}

// IsBoolFlag lets the flag be passed without a value, like the reference's own
// store_true/store_false pair.
func (f *accessLogFlag) IsBoolFlag() bool { return true }

// parseBoolValue accepts the bare form (no value) and an explicit one.
func parseBoolValue(raw string, bare bool) (bool, error) {
	switch raw {
	case "":
		return bare, nil
	case "true", "1", "TRUE", "True":
		return true, nil
	case "false", "0", "FALSE", "False":
		return false, nil
	}
	return false, fmt.Errorf("invalid boolean %q", raw)
}

func optionalString(fs *flag.FlagSet, name string, value *string) *string {
	if !flagWasSet(fs, name) {
		return nil
	}
	return value
}

func optionalInt(fs *flag.FlagSet, name string, value *int) *int {
	if !flagWasSet(fs, name) {
		return nil
	}
	return value
}

func optionalBool(fs *flag.FlagSet, name string, value *bool) *bool {
	if !flagWasSet(fs, name) {
		return nil
	}
	return value
}

func flagWasSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}
