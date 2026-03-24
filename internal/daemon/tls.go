package daemon

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

const (
	caCertFile = "ca-cert.pem"
	caKeyFile  = "ca-key.pem"
	maxCache   = 1024
)

// CA holds a local certificate authority for generating on-demand leaf certs.
type CA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
	mu      sync.RWMutex
	cache   map[string]*tls.Certificate
}

// LoadOrCreateCA loads an existing CA from caDir, or generates a new one if
// the cert/key files do not exist.
func LoadOrCreateCA(caDir string) (*CA, error) {
	certPath := filepath.Join(caDir, caCertFile)
	keyPath := filepath.Join(caDir, caKeyFile)

	certExists := fileExists(certPath)
	keyExists := fileExists(keyPath)

	if certExists && keyExists {
		return loadCA(certPath, keyPath)
	}

	if certExists != keyExists {
		slog.Warn("ca: partial CA files found, regenerating",
			"cert_exists", certExists,
			"key_exists", keyExists,
		)
	}

	return createCA(caDir)
}

// loadCA reads existing PEM files and parses the CA certificate and key.
func loadCA(certPath, keyPath string) (*CA, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("reading CA cert: %w", err)
	}

	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("reading CA key: %w", err)
	}

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, fmt.Errorf("ca: failed to decode cert PEM from %s", certPath)
	}

	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: parsing certificate: %w", err)
	}

	// Check if cert is expired; if so, warn and regenerate.
	if time.Now().After(cert.NotAfter) {
		slog.Warn("ca: certificate expired, regenerating", "expired", cert.NotAfter)
		return createCA(filepath.Dir(certPath))
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("ca: failed to decode key PEM from %s", keyPath)
	}

	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: parsing private key: %w", err)
	}

	slog.Info("ca: loaded existing CA", "subject", cert.Subject.CommonName, "expires", cert.NotAfter.Format("2006-01-02"))

	return &CA{
		cert:    cert,
		key:     key,
		certPEM: certPEM,
		cache:   make(map[string]*tls.Certificate),
	}, nil
}

// createCA generates a new ECDSA P-256 CA and writes PEM files to caDir.
func createCA(caDir string) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ca: generating key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("ca: generating serial: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "Eden Dev CA",
			Organization: []string{"Eden Development"},
		},
		NotBefore:             time.Now().Add(-1 * time.Hour), // back-date slightly
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("ca: creating certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("ca: parsing created certificate: %w", err)
	}

	// Encode PEM.
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("ca: marshalling key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	// Write files.
	if err := os.MkdirAll(caDir, 0o755); err != nil {
		return nil, fmt.Errorf("ca: creating directory: %w", err)
	}

	if err := os.WriteFile(filepath.Join(caDir, caCertFile), certPEM, 0o644); err != nil {
		return nil, fmt.Errorf("ca: writing cert: %w", err)
	}
	if err := os.WriteFile(filepath.Join(caDir, caKeyFile), keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("ca: writing key: %w", err)
	}

	slog.Info("ca: generated new CA", "dir", caDir, "expires", cert.NotAfter.Format("2006-01-02"))

	return &CA{
		cert:    cert,
		key:     key,
		certPEM: certPEM,
		cache:   make(map[string]*tls.Certificate),
	}, nil
}

// GetCertificate returns a TLS certificate for the given hostname. It is
// designed to be used as tls.Config.GetCertificate. Certificates are generated
// on demand and cached.
func (ca *CA) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	hostname := hello.ServerName
	if hostname == "" {
		return nil, fmt.Errorf("ca: no SNI hostname in TLS handshake")
	}

	// Check cache with read lock.
	ca.mu.RLock()
	if cached, ok := ca.cache[hostname]; ok {
		ca.mu.RUnlock()
		return cached, nil
	}
	ca.mu.RUnlock()

	// Generate new leaf cert.
	leafCert, err := ca.generateLeaf(hostname)
	if err != nil {
		return nil, err
	}

	// Store in cache with write lock.
	ca.mu.Lock()
	// Evict half the cache if at capacity.
	if len(ca.cache) >= maxCache {
		count := 0
		for k := range ca.cache {
			delete(ca.cache, k)
			count++
			if count >= maxCache/2 {
				break
			}
		}
		slog.Debug("ca: evicted cache entries", "evicted", count)
	}
	ca.cache[hostname] = leafCert
	ca.mu.Unlock()

	slog.Debug("ca: generated leaf cert", "hostname", hostname)
	return leafCert, nil
}

// generateLeaf creates a new leaf certificate signed by the CA for the given hostname.
func (ca *CA) generateLeaf(hostname string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ca: generating leaf key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("ca: generating leaf serial: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: hostname,
		},
		NotBefore: time.Now().Add(-1 * time.Hour),
		NotAfter:  time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
		DNSNames: []string{hostname},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, fmt.Errorf("ca: creating leaf certificate: %w", err)
	}

	tlsCert := &tls.Certificate{
		Certificate: [][]byte{certDER, ca.cert.Raw},
		PrivateKey:  key,
	}

	return tlsCert, nil
}

// CertPEMPath returns the path to the CA certificate PEM file.
func (ca *CA) CertPEMPath(caDir string) string {
	return filepath.Join(caDir, caCertFile)
}

// TrustCA adds the CA certificate to the macOS login keychain as a trusted
// root. This operation may prompt for the user's admin password.
func TrustCA(certPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("ca: getting home directory: %w", err)
	}

	keychainPath := filepath.Join(home, "Library", "Keychains", "login.keychain-db")

	fmt.Println("  Trusting CA certificate (you may be prompted for your admin password)...")

	cmd := exec.Command("security", "add-trusted-cert",
		"-r", "trustRoot",
		"-k", keychainPath,
		certPath,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ca: trusting certificate failed: %w\n"+
			"  Manual trust: security add-trusted-cert -r trustRoot -k %s %s",
			err, keychainPath, certPath)
	}

	slog.Info("ca: certificate trusted in macOS keychain")
	return nil
}
