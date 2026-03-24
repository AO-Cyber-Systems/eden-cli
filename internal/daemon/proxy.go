package daemon

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"time"
)

// CertProvider supplies TLS certificates dynamically per-ClientHello.
// The TLS subsystem (TRD-02) implements this interface.
type CertProvider interface {
	GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error)
}

// Proxy is an HTTP/HTTPS reverse proxy with Host-based routing.
// It routes requests from <name>.eden.test to the corresponding workspace
// backend port, with WebSocket passthrough via httputil.ReverseProxy.
type Proxy struct {
	state     *State
	certs     CertProvider
	domain    string // e.g. "eden.test"
	httpPort  int    // 80 or 9280
	httpsPort int    // 443 or 9283
	httpSrv   *http.Server
	httpsSrv  *http.Server
	startTime time.Time

	// OnRequest is an optional callback for process pool integration (TRD-04).
	// Called with the workspace name before proxying. Returns the backend port
	// or an error. If nil, the proxy looks up the port from State directly.
	OnRequest func(ctx context.Context, name string) (int, error)
}

// NewProxy creates a new Proxy. The certs parameter may be nil, in which case
// the HTTPS server will not be started (useful for testing HTTP-only).
func NewProxy(state *State, certs CertProvider, domain string, httpPort, httpsPort int) *Proxy {
	return &Proxy{
		state:     state,
		certs:     certs,
		domain:    domain,
		httpPort:  httpPort,
		httpsPort: httpsPort,
		startTime: time.Now(),
	}
}

// ListenAndServe starts both the HTTP redirect server and the HTTPS proxy
// server. It blocks until both servers have stopped or ctx is cancelled.
func (p *Proxy) ListenAndServe(ctx context.Context) error {
	errCh := make(chan error, 2)

	// HTTP server: redirects to HTTPS (except health check).
	p.httpSrv = &http.Server{
		Addr:              fmt.Sprintf(":%d", p.httpPort),
		Handler:           http.HandlerFunc(p.handleHTTP),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("HTTP redirect server starting", "port", p.httpPort)
		if err := p.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("HTTP server: %w", err)
			return
		}
		errCh <- nil
	}()

	// HTTPS server: reverse proxy with TLS.
	if p.certs != nil {
		tlsCfg := &tls.Config{
			GetCertificate: p.certs.GetCertificate,
			MinVersion:     tls.VersionTLS12,
		}
		p.httpsSrv = &http.Server{
			Addr:              fmt.Sprintf(":%d", p.httpsPort),
			Handler:           http.HandlerFunc(p.handleHTTPS),
			TLSConfig:         tlsCfg,
			ReadHeaderTimeout: 10 * time.Second,
		}

		go func() {
			slog.Info("HTTPS proxy server starting", "port", p.httpsPort)
			// ListenAndServeTLS with empty cert/key because tls.Config.GetCertificate handles it.
			if err := p.httpsSrv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				errCh <- fmt.Errorf("HTTPS server: %w", err)
				return
			}
			errCh <- nil
		}()
	}

	// Wait for context cancellation.
	<-ctx.Done()
	return nil
}

// Shutdown gracefully stops both HTTP and HTTPS servers.
func (p *Proxy) Shutdown(ctx context.Context) error {
	var firstErr error
	if p.httpSrv != nil {
		if err := p.httpSrv.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("HTTP server shutdown: %w", err)
		}
	}
	if p.httpsSrv != nil {
		if err := p.httpsSrv.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("HTTPS server shutdown: %w", err)
		}
	}
	return firstErr
}

// handleHTTP serves the HTTP (non-TLS) listener. It redirects all requests to
// the HTTPS equivalent, except for the /_eden/health endpoint.
func (p *Proxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	// Health check endpoint — available on HTTP for monitoring.
	if r.URL.Path == "/_eden/health" {
		p.serveHealth(w, r)
		return
	}

	// 301 redirect to HTTPS.
	host := r.Host
	// Strip port from host if present.
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	target := "https://" + host
	if p.httpsPort != 443 {
		target = fmt.Sprintf("https://%s:%d", host, p.httpsPort)
	}
	target += r.URL.RequestURI()

	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

// handleHTTPS is the main request handler for the HTTPS proxy server.
func (p *Proxy) handleHTTPS(w http.ResponseWriter, r *http.Request) {
	// Health check endpoint.
	if r.URL.Path == "/_eden/health" {
		p.serveHealth(w, r)
		return
	}

	// Extract workspace name from host.
	name, err := p.extractWorkspace(r.Host)
	if err != nil {
		// Host doesn't match *.eden.test pattern.
		p.serveError(w, http.StatusBadRequest, "Invalid Host",
			fmt.Sprintf("Host %q does not match *.%s", r.Host, p.domain))
		return
	}

	// No workspace prefix — serve status dashboard.
	if name == "" {
		p.serveDashboard(w, r)
		return
	}

	// Resolve backend port.
	port, resolveErr := p.resolvePort(r.Context(), name)
	if resolveErr != nil {
		p.serveError(w, http.StatusServiceUnavailable, "Workspace Not Started",
			fmt.Sprintf("Workspace %q is not running. Link it with: eden dev link %s /path/to/app", name, name))
		return
	}
	if port == 0 {
		p.serveError(w, http.StatusNotFound, "Workspace Not Found",
			fmt.Sprintf("No workspace named %q is registered.", name))
		return
	}

	// Create reverse proxy to backend.
	target := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("127.0.0.1:%d", port),
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = r.Host // preserve original Host header
		},
		FlushInterval: -1, // stream responses (SSE, gRPC-web)
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			slog.Error("proxy backend error",
				"workspace", name,
				"target", target.String(),
				"error", err,
			)
			p.serveError(rw, http.StatusBadGateway, "Backend Unavailable",
				fmt.Sprintf("Could not reach workspace %q at port %d: %v", name, port, err))
		},
	}

	proxy.ServeHTTP(w, r)
}

// extractWorkspace parses the workspace name from a Host header value.
// For "biz.eden.test" it returns "biz". For "api.biz.eden.test" it returns
// "api.biz". For "eden.test" (no prefix) it returns "". Returns an error
// if the host doesn't end with the configured domain.
func (p *Proxy) extractWorkspace(host string) (string, error) {
	// Strip port if present.
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	host = strings.ToLower(host)
	domain := strings.ToLower(p.domain)

	// Exact match: eden.test -> dashboard.
	if host == domain {
		return "", nil
	}

	// Must end with .eden.test
	suffix := "." + domain
	if !strings.HasSuffix(host, suffix) {
		return "", fmt.Errorf("host %q does not end with %q", host, suffix)
	}

	// Extract everything before .eden.test
	name := strings.TrimSuffix(host, suffix)
	if name == "" {
		return "", fmt.Errorf("empty workspace name in host %q", host)
	}

	return name, nil
}

// resolvePort determines the backend port for a workspace. If an OnRequest
// callback is set (process pool from TRD-04), it's called first. Otherwise
// the port is looked up directly from State.
func (p *Proxy) resolvePort(ctx context.Context, name string) (int, error) {
	if p.OnRequest != nil {
		return p.OnRequest(ctx, name)
	}

	ws := p.state.Get(name)
	if ws == nil {
		return 0, fmt.Errorf("workspace %q not found", name)
	}
	if ws.Port == 0 {
		return 0, fmt.Errorf("workspace %q has no port assigned", name)
	}
	return ws.Port, nil
}

// serveHealth returns a JSON health check response.
func (p *Proxy) serveHealth(w http.ResponseWriter, _ *http.Request) {
	workspaces := p.state.All()
	uptime := time.Since(p.startTime).Truncate(time.Second)

	resp := struct {
		Status     string `json:"status"`
		Workspaces int    `json:"workspaces"`
		Uptime     string `json:"uptime"`
	}{
		Status:     "ok",
		Workspaces: len(workspaces),
		Uptime:     formatDuration(uptime),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// formatDuration formats a duration as "Xh Ym Zs".
func formatDuration(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// serveDashboard renders the status dashboard at eden.test (no workspace prefix).
func (p *Proxy) serveDashboard(w http.ResponseWriter, _ *http.Request) {
	workspaces := p.state.All()
	sort.Slice(workspaces, func(i, j int) bool {
		return workspaces[i].Name < workspaces[j].Name
	})

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	dashboardTmpl.Execute(w, struct {
		Domain     string
		Workspaces []*WorkspaceState
		Uptime     string
		HTTPSPort  int
	}{
		Domain:     p.domain,
		Workspaces: workspaces,
		Uptime:     formatDuration(time.Since(p.startTime).Truncate(time.Second)),
		HTTPSPort:  p.httpsPort,
	})
}

// serveError renders an HTML error page with a list of available workspaces.
func (p *Proxy) serveError(w http.ResponseWriter, code int, title, message string) {
	workspaces := p.state.All()
	sort.Slice(workspaces, func(i, j int) bool {
		return workspaces[i].Name < workspaces[j].Name
	})

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	errorTmpl.Execute(w, struct {
		Code       int
		Title      string
		Message    string
		Domain     string
		Workspaces []*WorkspaceState
		HTTPSPort  int
	}{
		Code:       code,
		Title:      title,
		Message:    message,
		Domain:     p.domain,
		Workspaces: workspaces,
		HTTPSPort:  p.httpsPort,
	})
}

// workspaceURL returns the full URL for a workspace, including non-standard ports.
func workspaceURL(name, domain string, httpsPort int) string {
	if httpsPort == 443 {
		return fmt.Sprintf("https://%s.%s", name, domain)
	}
	return fmt.Sprintf("https://%s.%s:%d", name, domain, httpsPort)
}

var dashboardTmpl = template.Must(template.New("dashboard").Funcs(template.FuncMap{
	"workspaceURL": workspaceURL,
}).Parse(`<!DOCTYPE html>
<html>
<head>
  <title>Eden Dev Server</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; max-width: 800px; margin: 40px auto; padding: 0 20px; color: #333; }
    h1 { color: #1a1a2e; }
    table { width: 100%; border-collapse: collapse; margin-top: 20px; }
    th, td { text-align: left; padding: 10px 12px; border-bottom: 1px solid #e0e0e0; }
    th { background: #f5f5f5; font-weight: 600; }
    a { color: #0066cc; text-decoration: none; }
    a:hover { text-decoration: underline; }
    .status { display: inline-block; padding: 2px 8px; border-radius: 4px; font-size: 13px; }
    .status-running { background: #d4edda; color: #155724; }
    .status-stopped { background: #f8d7da; color: #721c24; }
    .status-starting { background: #fff3cd; color: #856404; }
    .status-idle { background: #e2e3e5; color: #383d41; }
    .meta { color: #666; font-size: 14px; margin-top: 8px; }
  </style>
</head>
<body>
  <h1>Eden Dev Server</h1>
  <p class="meta">Domain: {{.Domain}} | Uptime: {{.Uptime}} | Workspaces: {{len .Workspaces}}</p>
  {{if .Workspaces}}
  <table>
    <tr><th>Workspace</th><th>Status</th><th>Port</th><th>URL</th></tr>
    {{range .Workspaces}}
    <tr>
      <td>{{.Name}}</td>
      <td><span class="status status-{{.Status}}">{{.Status}}</span></td>
      <td>{{if .Port}}{{.Port}}{{else}}-{{end}}</td>
      <td><a href="{{workspaceURL .Name $.Domain $.HTTPSPort}}">{{workspaceURL .Name $.Domain $.HTTPSPort}}</a></td>
    </tr>
    {{end}}
  </table>
  {{else}}
  <p>No workspaces registered. Link one with:</p>
  <pre>eden dev link myapp /path/to/app</pre>
  {{end}}
</body>
</html>
`))

var errorTmpl = template.Must(template.New("error").Funcs(template.FuncMap{
	"workspaceURL": workspaceURL,
}).Parse(`<!DOCTYPE html>
<html>
<head>
  <title>{{.Title}} - Eden Dev Server</title>
  <style>
    body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; max-width: 800px; margin: 40px auto; padding: 0 20px; color: #333; }
    h1 { color: #c0392b; }
    .message { background: #f8f9fa; padding: 16px; border-radius: 8px; border-left: 4px solid #c0392b; margin: 20px 0; }
    h2 { color: #1a1a2e; font-size: 18px; margin-top: 30px; }
    table { width: 100%; border-collapse: collapse; margin-top: 10px; }
    th, td { text-align: left; padding: 8px 12px; border-bottom: 1px solid #e0e0e0; }
    th { background: #f5f5f5; font-weight: 600; }
    a { color: #0066cc; text-decoration: none; }
    a:hover { text-decoration: underline; }
  </style>
</head>
<body>
  <h1>{{.Code}} {{.Title}}</h1>
  <div class="message">{{.Message}}</div>
  {{if .Workspaces}}
  <h2>Available Workspaces</h2>
  <table>
    <tr><th>Workspace</th><th>URL</th></tr>
    {{range .Workspaces}}
    <tr>
      <td>{{.Name}}</td>
      <td><a href="{{workspaceURL .Name $.Domain $.HTTPSPort}}">{{workspaceURL .Name $.Domain $.HTTPSPort}}</a></td>
    </tr>
    {{end}}
  </table>
  {{end}}
</body>
</html>
`))
