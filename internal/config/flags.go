package config

import (
	"os"
	"strings"
)

// lookupEnv resolves one configuration key: the process environment first —
// which the .env file leads, because loading it sets the process environment —
// and the YAML configuration file behind both.
func lookupEnv(key string) (string, bool) {
	if value, ok := lookupConfigFile(key); ok {
		return value, true
	}
	return os.LookupEnv(key)
}

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
	ForwardedAllowIPs      *string
	Reload                 *bool
	IdleTimeout            *int
	SSLKeyFile             *string
	SSLCertFile            *string
	Daemon                 *bool
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
	if flags.Reload != nil {
		cfg.Reload = *flags.Reload
	}
	if flags.IdleTimeout != nil {
		cfg.IdleTimeout = *flags.IdleTimeout
	}
	if flags.ForwardedAllowIPs != nil {
		if list := splitAllowList(*flags.ForwardedAllowIPs); len(list) > 0 {
			cfg.ForwardedAllowIPs = list
		}
	}
	// Daemon mode resolves its own bind address, the way the reference does: the
	// loopback default applies unless a host was configured explicitly (the flag
	// here, or WENYUAN_API_HOST in the environment), and the daemon port
	// applies unless --port was passed — a port exported for foreground runs
	// does not move the daemon.
	if cfg.Daemon {
		_, configuredHost := base.rawEnv[EnvHost]
		explicitHost := flags.Host != nil && strings.TrimSpace(*flags.Host) != ""
		if !explicitHost && !configuredHost {
			cfg.Host = DefaultDaemonHost
		}
		if flags.Port == nil || *flags.Port <= 0 {
			cfg.Port = DefaultDaemonPort
		}
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

// splitAllowList splits a comma-separated trusted-proxy list, dropping blank
// entries. A value of "*" is kept as-is: it means "trust every peer".
func splitAllowList(value string) []string {
	out := make([]string, 0, 2)
	for _, entry := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
