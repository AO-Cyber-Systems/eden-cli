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

	// Start DNS server.
	d.dns = NewDNSServer(d.Config.Domain, d.Config.DNSPort)
	dnsErrCh := make(chan error, 1)
	go func() {
		if err := d.dns.ListenAndServe(); err != nil {
			slog.Error("dns server error", "error", err)
			dnsErrCh <- err
		}
	}()

	slog.Info("eden daemon running")

	// Block until context is cancelled or DNS fails.
	select {
	case <-ctx.Done():
		slog.Info("eden daemon shutting down")
	case err := <-dnsErrCh:
		slog.Error("eden daemon: DNS server failed, shutting down", "error", err)
		_ = d.Shutdown()
		return fmt.Errorf("dns server: %w", err)
	}

	return d.Shutdown()
}

// Shutdown gracefully stops all daemon subsystems.
func (d *Daemon) Shutdown() error {
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
