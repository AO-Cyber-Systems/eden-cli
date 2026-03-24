package daemon

import (
	"context"
	"log/slog"
	"time"
)

// Config holds the daemon's runtime configuration.
type Config struct {
	DNSPort     int           // default 9253
	HTTPPort    int           // default 80
	HTTPSPort   int           // default 443
	IdleTimeout time.Duration // default 15min
	Domain      string        // default "eden.test"
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		DNSPort:     9253,
		HTTPPort:    80,
		HTTPSPort:   443,
		IdleTimeout: 15 * time.Minute,
		Domain:      "eden.test",
	}
}

// Daemon is the main eden dev server daemon. It manages DNS resolution, TLS
// termination, reverse proxying, and workspace process lifecycle. DNS, proxy,
// and pool subsystems will be wired in subsequent TRDs.
type Daemon struct {
	Config *Config
	State  *State
}

// Run starts the daemon and blocks until ctx is cancelled. Subsystem startup
// (DNS, proxy, process pool) will be added by later TRDs; for now it logs
// startup and waits for shutdown.
func (d *Daemon) Run(ctx context.Context) error {
	slog.Info("eden daemon starting",
		"domain", d.Config.Domain,
		"dns_port", d.Config.DNSPort,
		"http_port", d.Config.HTTPPort,
		"https_port", d.Config.HTTPSPort,
	)

	// Block until context is cancelled (e.g. SIGINT/SIGTERM).
	<-ctx.Done()

	slog.Info("eden daemon shutting down")
	return d.Shutdown()
}

// Shutdown gracefully stops all daemon subsystems. Currently a no-op; will be
// expanded as subsystems are added.
func (d *Daemon) Shutdown() error {
	slog.Info("eden daemon stopped")
	return nil
}
