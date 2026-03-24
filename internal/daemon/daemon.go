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
	HighPorts   bool          // use 9280/9283 instead of 80/443
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

// EffectiveHTTPPort returns the HTTP port, accounting for HighPorts mode.
func (c *Config) EffectiveHTTPPort() int {
	if c.HighPorts {
		return 9280
	}
	return c.HTTPPort
}

// EffectiveHTTPSPort returns the HTTPS port, accounting for HighPorts mode.
func (c *Config) EffectiveHTTPSPort() int {
	if c.HighPorts {
		return 9283
	}
	return c.HTTPSPort
}

// Daemon is the main eden dev server daemon. It manages DNS resolution, TLS
// termination, reverse proxying, and workspace process lifecycle.
type Daemon struct {
	Config *Config
	State  *State
	proxy  *Proxy

	// CertProvider is set by the TLS subsystem (TRD-02) before Run() is called.
	// If nil, the proxy runs without HTTPS (HTTP-only mode for testing).
	CertProvider CertProvider
}

// Run starts the daemon and blocks until ctx is cancelled. It starts the
// reverse proxy servers and waits for shutdown.
func (d *Daemon) Run(ctx context.Context) error {
	httpPort := d.Config.EffectiveHTTPPort()
	httpsPort := d.Config.EffectiveHTTPSPort()

	slog.Info("eden daemon starting",
		"domain", d.Config.Domain,
		"dns_port", d.Config.DNSPort,
		"http_port", httpPort,
		"https_port", httpsPort,
		"high_ports", d.Config.HighPorts,
	)

	// Create and start the reverse proxy.
	d.proxy = NewProxy(d.State, d.CertProvider, d.Config.Domain, httpPort, httpsPort)

	// Start proxy in background goroutine.
	proxyErrCh := make(chan error, 1)
	go func() {
		proxyErrCh <- d.proxy.ListenAndServe(ctx)
	}()

	if d.CertProvider != nil {
		slog.Info("HTTPS proxy listening", "port", httpsPort)
	} else {
		slog.Warn("HTTPS proxy disabled (no TLS certificates available)")
	}
	slog.Info("HTTP redirect listening", "port", httpPort)

	// Block until context is cancelled (e.g. SIGINT/SIGTERM).
	<-ctx.Done()

	slog.Info("eden daemon shutting down")
	return d.Shutdown()
}

// Shutdown gracefully stops all daemon subsystems with a 5-second timeout.
func (d *Daemon) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var firstErr error

	// Shutdown proxy servers.
	if d.proxy != nil {
		if err := d.proxy.Shutdown(ctx); err != nil {
			slog.Error("proxy shutdown error", "error", err)
			firstErr = err
		}
	}

	slog.Info("eden daemon stopped")
	return firstErr
}
