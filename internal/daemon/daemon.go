package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"
)

// Config holds the daemon's runtime configuration.
type Config struct {
	DNSPort     int           // default 9253
	HTTPPort    int           // default 80
	HTTPSPort   int           // default 443
	IdleTimeout time.Duration // default 15min
	Domain      string        // default "eden.test"
	HighPorts   bool          // use unprivileged ports (9280/9283) instead of 80/443
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
// termination, reverse proxying, and workspace process lifecycle.
type Daemon struct {
	Config *Config
	State  *State
	dns    *DNSServer
	ca     *CA
	proxy  *Proxy
	pool   *Pool
}

// Run starts the daemon and blocks until ctx is cancelled. It starts the DNS
// server and loads (or creates) the local CA for TLS certificate generation.
func (d *Daemon) Run(ctx context.Context) error {
	slog.Info("eden daemon starting",
		"domain", d.Config.Domain,
		"dns_port", d.Config.DNSPort,
		"http_port", d.Config.HTTPPort,
		"https_port", d.Config.HTTPSPort,
	)

	// Ensure config directories exist.
	if err := EnsureConfigDirs(); err != nil {
		return fmt.Errorf("creating config dirs: %w", err)
	}

	// Load or create CA.
	caDir, err := CADir()
	if err != nil {
		return fmt.Errorf("resolving CA directory: %w", err)
	}
	ca, err := LoadOrCreateCA(caDir)
	if err != nil {
		return fmt.Errorf("loading CA: %w", err)
	}
	d.ca = ca
	slog.Info("eden daemon CA ready", "cert", filepath.Join(caDir, caCertFile))

	// Determine effective ports.
	httpPort := d.Config.HTTPPort
	httpsPort := d.Config.HTTPSPort
	if d.Config.HighPorts {
		httpPort = 9280
		httpsPort = 9283
	}

	// Start DNS server.
	d.dns = NewDNSServer(d.Config.Domain, d.Config.DNSPort)
	dnsErrCh := make(chan error, 1)
	go func() {
		if err := d.dns.ListenAndServe(); err != nil {
			slog.Error("dns server error", "error", err)
			dnsErrCh <- err
		}
	}()

	// Create reverse proxy.
	d.proxy = NewProxy(d.State, ca, d.Config.Domain, httpPort, httpsPort)

	// Create process pool and wire it into the proxy.
	logsDir, err := LogsDir()
	if err != nil {
		return fmt.Errorf("resolving logs directory: %w", err)
	}
	d.pool = NewPool(d.State, logsDir, d.Config.IdleTimeout)
	d.proxy.OnRequest = d.pool.EnsureRunning

	// Start idle monitor.
	go d.pool.StartIdleMonitor(ctx)

	// Start proxy servers.
	proxyErrCh := make(chan error, 1)
	go func() {
		if err := d.proxy.ListenAndServe(ctx); err != nil {
			slog.Error("proxy server error", "error", err)
			proxyErrCh <- err
		}
	}()

	slog.Info("eden daemon running",
		"http_port", httpPort,
		"https_port", httpsPort,
	)

	// Block until context is cancelled or a subsystem fails.
	select {
	case <-ctx.Done():
		slog.Info("eden daemon shutting down")
	case err := <-dnsErrCh:
		slog.Error("eden daemon: DNS server failed, shutting down", "error", err)
		_ = d.Shutdown()
		return fmt.Errorf("dns server: %w", err)
	case err := <-proxyErrCh:
		slog.Error("eden daemon: proxy server failed, shutting down", "error", err)
		_ = d.Shutdown()
		return fmt.Errorf("proxy server: %w", err)
	}

	return d.Shutdown()
}

// Shutdown gracefully stops all daemon subsystems. Process pool is stopped
// first to ensure clean process cleanup before proxy and DNS shut down.
func (d *Daemon) Shutdown() error {
	// Stop all workspace processes first.
	if d.pool != nil {
		if err := d.pool.StopAll(); err != nil {
			slog.Error("pool shutdown error", "error", err)
		}
	}

	// Stop proxy servers.
	if d.proxy != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := d.proxy.Shutdown(shutdownCtx); err != nil {
			slog.Error("proxy shutdown error", "error", err)
		}
	}

	// Stop DNS server.
	if d.dns != nil {
		if err := d.dns.Shutdown(); err != nil {
			slog.Error("dns shutdown error", "error", err)
		}
	}
	slog.Info("eden daemon stopped")
	return nil
}

// CA returns the daemon's certificate authority, or nil if not yet loaded.
func (d *Daemon) CA() *CA {
	return d.ca
}
