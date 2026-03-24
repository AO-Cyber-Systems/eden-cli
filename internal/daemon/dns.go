package daemon

import (
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"

	"github.com/miekg/dns"
)

// DNSServer resolves *.eden.test queries to 127.0.0.1 (A) and ::1 (AAAA).
// It runs on both UDP and TCP on the configured port.
type DNSServer struct {
	domain string // FQDN with trailing dot, e.g. "eden.test."
	port   int
	udp    *dns.Server
	tcp    *dns.Server
}

// NewDNSServer creates a DNS server that resolves queries for the given domain.
// The domain should be provided without a trailing dot (e.g. "eden.test").
func NewDNSServer(domain string, port int) *DNSServer {
	// Ensure trailing dot for DNS FQDN format.
	fqdn := dns.Fqdn(domain)

	s := &DNSServer{
		domain: fqdn,
		port:   port,
	}

	mux := dns.NewServeMux()
	mux.HandleFunc(fqdn, s.handleQuery)

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	s.udp = &dns.Server{Addr: addr, Net: "udp", Handler: mux}
	s.tcp = &dns.Server{Addr: addr, Net: "tcp", Handler: mux}

	return s
}

// handleQuery responds to DNS queries for the configured domain.
func (s *DNSServer) handleQuery(w dns.ResponseWriter, r *dns.Msg) {
	msg := new(dns.Msg)
	msg.SetReply(r)
	msg.Authoritative = true

	for _, q := range r.Question {
		name := strings.ToLower(q.Name)

		// Only respond to queries within our domain.
		if !dns.IsSubDomain(s.domain, name) && name != s.domain {
			continue
		}

		switch q.Qtype {
		case dns.TypeA:
			msg.Answer = append(msg.Answer, &dns.A{
				Hdr: dns.RR_Header{
					Name:   q.Name,
					Rrtype: dns.TypeA,
					Class:  dns.ClassINET,
					Ttl:    60,
				},
				A: net.ParseIP("127.0.0.1"),
			})
			slog.Debug("dns: A query", "name", q.Name, "answer", "127.0.0.1")

		case dns.TypeAAAA:
			msg.Answer = append(msg.Answer, &dns.AAAA{
				Hdr: dns.RR_Header{
					Name:   q.Name,
					Rrtype: dns.TypeAAAA,
					Class:  dns.ClassINET,
					Ttl:    60,
				},
				AAAA: net.ParseIP("::1"),
			})
			slog.Debug("dns: AAAA query", "name", q.Name, "answer", "::1")

		default:
			// For unsupported query types, respond with NXDOMAIN.
			msg.Rcode = dns.RcodeNameError
			slog.Debug("dns: unsupported query type", "name", q.Name, "type", dns.TypeToString[q.Qtype])
		}
	}

	if err := w.WriteMsg(msg); err != nil {
		slog.Error("dns: failed to write response", "error", err)
	}
}

// ListenAndServe starts both UDP and TCP listeners. It blocks until both
// servers have started, then returns the first error from either.
func (s *DNSServer) ListenAndServe() error {
	errCh := make(chan error, 2)

	go func() {
		slog.Info("dns: starting UDP listener", "addr", s.udp.Addr)
		errCh <- s.udp.ListenAndServe()
	}()

	go func() {
		slog.Info("dns: starting TCP listener", "addr", s.tcp.Addr)
		errCh <- s.tcp.ListenAndServe()
	}()

	// Return the first error from either server.
	err := <-errCh
	if err != nil {
		return fmt.Errorf("dns server failed on port %d: %w (check: lsof -i :%d)", s.port, err, s.port)
	}
	return nil
}

// Shutdown gracefully stops both DNS servers.
func (s *DNSServer) Shutdown() error {
	var errs []error
	if s.udp != nil {
		if err := s.udp.Shutdown(); err != nil {
			errs = append(errs, fmt.Errorf("udp shutdown: %w", err))
		}
	}
	if s.tcp != nil {
		if err := s.tcp.Shutdown(); err != nil {
			errs = append(errs, fmt.Errorf("tcp shutdown: %w", err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("dns shutdown errors: %v", errs)
	}
	slog.Info("dns: server stopped")
	return nil
}
