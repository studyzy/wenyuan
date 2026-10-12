package config

import (
	"os"
	"strings"
)

func lookupEnv(key string) (string, bool) { return os.LookupEnv(key) }

// EntrypointName is the stable process entrypoint name recorded in the
// approved compatibility surface. It is descriptive metadata; the actual
// artifact produced by cmd/server is a compiled binary that may be installed
// under this name.
const EntrypointName = "wenyuan"

// FlagValues holds the raw command-line flag values after parsing. A nil
// pointer means the flag was not supplied, which allows the resolver to fall
// back to environment values and then defaults.
type FlagValues struct {
	Host                   *string
	Port                   *int
	LogLevel               *string
	Workers                *int
	AccessLog              *bool
	ProxyHeaders           *bool
	SSLKeyFile             *string
	SSLCertFile            *string
	Daemon                 *bool
	NullDropFields         *bool
	DatabaseURL            *string
	DatabaseSchema         *string
	RunMigrationsOnStartup *bool
	MCPEnabled             *bool
	EnableBankConfigAPI    *bool
}

// ApplyFlags resolves the final configuration by layering supplied flags over
// an environment-resolved base. Precedence is flag, then environment, then
// built-in default.
func ApplyFlags(base Config, flags FlagValues) Config {
	cfg := base
	if flags.Host != nil && strings.TrimSpace(*flags.Host) != "" {
		cfg.Host = strings.TrimSpace(*flags.Host)
	}
	if flags.Port != nil && *flags.Port > 0 {
		cfg.Port = *flags.Port
	}
	if flags.Daemon != nil {
		cfg.Daemon = *flags.Daemon
	}
	// In daemon mode the bind address defaults to loopback unless the
	// operator explicitly supplied a non-default host.
	if cfg.Daemon && (flags.Host == nil || strings.TrimSpace(*flags.Host) == "") {
		cfg.Host = applyDaemonHost(cfg.Host)
	}
	if flags.LogLevel != nil && *flags.LogLevel != "" {
		cfg.LogLevel = *flags.LogLevel
	}
	if flags.Workers != nil && *flags.Workers > 0 {
		cfg.Workers = *flags.Workers
	}
	if flags.AccessLog != nil {
		cfg.AccessLog = *flags.AccessLog
	}
	if flags.ProxyHeaders != nil {
		cfg.ProxyHeaders = *flags.ProxyHeaders
	}
	if flags.SSLKeyFile != nil {
		cfg.SSLKeyFile = *flags.SSLKeyFile
	}
	if flags.SSLCertFile != nil {
		cfg.SSLCertFile = *flags.SSLCertFile
	}
	if flags.NullDropFields != nil {
		cfg.NullDropFields = *flags.NullDropFields
	}
	if flags.DatabaseURL != nil {
		cfg.DatabaseURL = strings.TrimSpace(*flags.DatabaseURL)
	}
	if flags.DatabaseSchema != nil && strings.TrimSpace(*flags.DatabaseSchema) != "" {
		cfg.DatabaseSchema = strings.TrimSpace(*flags.DatabaseSchema)
	}
	if flags.RunMigrationsOnStartup != nil {
		cfg.RunMigrationsOnStartup = *flags.RunMigrationsOnStartup
	}
	if flags.MCPEnabled != nil {
		cfg.MCPEnabled = *flags.MCPEnabled
	}
	if flags.EnableBankConfigAPI != nil {
		cfg.EnableBankConfigAPI = *flags.EnableBankConfigAPI
	}
	return cfg
}

// applyDaemonHost defaults the bind address to loopback in daemon mode unless
// the operator explicitly supplied a non-default host.
func applyDaemonHost(host string) string {
	if strings.TrimSpace(host) == "" || host == DefaultHost {
		return DefaultHost
	}
	return host
}
