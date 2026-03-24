package daemon

import (
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestDNSServerAQuery(t *testing.T) {
	port := findFreeUDPPort(t)
	srv := NewDNSServer("eden.test", port)

	go func() {
		if err := srv.ListenAndServe(); err != nil {
			// Server stopped — expected on shutdown.
		}
	}()
	// Give the server a moment to start.
	time.Sleep(100 * time.Millisecond)
	t.Cleanup(func() { srv.Shutdown() })

	// Query for an A record.
	msg := new(dns.Msg)
	msg.SetQuestion("foo.eden.test.", dns.TypeA)

	client := new(dns.Client)
	resp, _, err := client.Exchange(msg, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("DNS query failed: %v", err)
	}

	if len(resp.Answer) == 0 {
		t.Fatal("expected at least one answer, got none")
	}

	a, ok := resp.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("expected A record, got %T", resp.Answer[0])
	}

	if a.A.String() != "127.0.0.1" {
		t.Errorf("expected 127.0.0.1, got %s", a.A.String())
	}
}

func TestDNSServerAAAAQuery(t *testing.T) {
	port := findFreeUDPPort(t)
	srv := NewDNSServer("eden.test", port)

	go func() {
		if err := srv.ListenAndServe(); err != nil {
			// Server stopped — expected on shutdown.
		}
	}()
	time.Sleep(100 * time.Millisecond)
	t.Cleanup(func() { srv.Shutdown() })

	msg := new(dns.Msg)
	msg.SetQuestion("bar.eden.test.", dns.TypeAAAA)

	client := new(dns.Client)
	resp, _, err := client.Exchange(msg, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("DNS query failed: %v", err)
	}

	if len(resp.Answer) == 0 {
		t.Fatal("expected at least one answer, got none")
	}

	aaaa, ok := resp.Answer[0].(*dns.AAAA)
	if !ok {
		t.Fatalf("expected AAAA record, got %T", resp.Answer[0])
	}

	if aaaa.AAAA.String() != "::1" {
		t.Errorf("expected ::1, got %s", aaaa.AAAA.String())
	}
}

func TestDNSServerRootDomain(t *testing.T) {
	port := findFreeUDPPort(t)
	srv := NewDNSServer("eden.test", port)

	go func() {
		if err := srv.ListenAndServe(); err != nil {
			// Server stopped — expected on shutdown.
		}
	}()
	time.Sleep(100 * time.Millisecond)
	t.Cleanup(func() { srv.Shutdown() })

	msg := new(dns.Msg)
	msg.SetQuestion("eden.test.", dns.TypeA)

	client := new(dns.Client)
	resp, _, err := client.Exchange(msg, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("DNS query failed: %v", err)
	}

	if len(resp.Answer) == 0 {
		t.Fatal("expected answer for root domain")
	}

	a, ok := resp.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("expected A record, got %T", resp.Answer[0])
	}

	if a.A.String() != "127.0.0.1" {
		t.Errorf("expected 127.0.0.1, got %s", a.A.String())
	}
}

// findFreeUDPPort finds an available UDP port for testing.
func findFreeUDPPort(t *testing.T) int {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	conn.Close()
	return port
}
