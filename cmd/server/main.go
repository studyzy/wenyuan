// Command wenyuan is the process entrypoint for the agent-memory HTTP
// API. It resolves configuration from the environment, composes the served
// application, and runs the HTTP server.
package main

import (
	"context"
	"flag"
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
	var (
		host         = flags.String("host", "", "bind host (overrides the environment and the default)")
		port         = flags.Int("port", 0, "bind port (overrides the environment and the default)")
		logLevel     = flags.String("log-level", "", "log level")
		workers      = flags.Int("workers", 0, "number of worker processes")
		accessLog    = flags.Bool("access-log", false, "emit an access log line per request")
		proxyHeaders = flags.Bool("proxy-headers", false, "honour forwarded proxy headers")
		sslKeyFile   = flags.String("ssl-keyfile", "", "TLS private key file")
		sslCertFile  = flags.String("ssl-certfile", "", "TLS certificate file")
		daemon       = flags.Bool("daemon", false, "run as a daemon and default the bind address to loopback")
	)
	if err := flags.Parse(args); err != nil {
		return err
	}

	if err := config.LoadDotenvFile(".env"); err != nil {
		return err
	}

	base := config.FromEnvironment()
	cfg := config.ApplyFlags(base, config.FlagValues{
		Host:         optionalString(flags, "host", host),
		Port:         optionalInt(flags, "port", port),
		LogLevel:     optionalString(flags, "log-level", logLevel),
		Workers:      optionalInt(flags, "workers", workers),
		AccessLog:    optionalBool(flags, "access-log", accessLog),
		ProxyHeaders: optionalBool(flags, "proxy-headers", proxyHeaders),
		SSLKeyFile:   optionalString(flags, "ssl-keyfile", sslKeyFile),
		SSLCertFile:  optionalString(flags, "ssl-certfile", sslCertFile),
		Daemon:       optionalBool(flags, "daemon", daemon),
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	slog.SetDefault(slog.New(monitoring.NewLogHandler(cfg)))
	defer stop()

	slog.Info("starting", "address", cfg.Address(), "entrypoint", config.EntrypointName)
	return app.Serve(ctx, cfg, app.Dependencies{}, app.ServeOptions{})
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
