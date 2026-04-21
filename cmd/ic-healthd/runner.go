package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	incus "github.com/lxc/incus/v6/client"
	incusApi "github.com/lxc/incus/v6/shared/api"
)

const (
	certFile  = "client.crt"
	keyFile   = "client.key"
	tokenFile = "token"
)

// Runner manages all health checkers.
type Runner struct {
	config *Config
	client incus.InstanceServer
}

// NewRunner creates a new runner with the given configuration.
func NewRunner(cfg *Config) (*Runner, error) {
	return &Runner{
		config: cfg,
	}, nil
}

// Run starts all health checkers and blocks until context is cancelled.
func (r *Runner) Run(ctx context.Context) error {
	client, err := r.connect()
	if err != nil {
		return fmt.Errorf("connecting to incus: %w", err)
	}
	r.client = client.UseProject(r.config.Projects[0])

	if r.config.Debug {
		log.Printf("connected to incus, project=%s", r.config.Projects[0])
	}

	if err := r.config.Discover(r.client); err != nil {
		log.Printf("service discovery had errors: %v", err)
	}

	var wg sync.WaitGroup
	for name, svc := range r.config.Services {
		checker := NewChecker(r.client, name, svc, r.config.Debug)
		wg.Add(1)
		go func() {
			defer wg.Done()
			checker.Run(ctx)
		}()

		if r.config.Debug {
			log.Printf("started checker for %s (interval=%s, retries=%d, restart=%v)",
				name, svc.Interval.Duration(), svc.Retries, svc.Restart)
		}
	}

	log.Printf("health daemon running, monitoring %d services", len(r.config.Services))

	wg.Wait()
	log.Println("all checkers stopped")
	return nil
}

// connect returns an authenticated Incus client.
//
// On first run, the persisted cert is missing: we generate one, register it
// with the one-time TrustToken, and persist it for subsequent runs.
// On restart, the persisted cert is reused and the token (already consumed) is ignored.
func (r *Runner) connect() (incus.InstanceServer, error) {
	certPath := filepath.Join(r.config.DataDir, certFile)
	keyPath := filepath.Join(r.config.DataDir, keyFile)

	if !fileExists(certPath) || !fileExists(keyPath) {
		if r.config.Debug {
			log.Println("no persisted cert; performing first-run registration")
		}
		if err := r.register(certPath, keyPath); err != nil {
			return nil, fmt.Errorf("first-run registration: %w", err)
		}
	} else if r.config.Debug {
		log.Println("reusing persisted cert from data dir")
	}

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("reading cert: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("reading key: %w", err)
	}

	return incus.ConnectIncus(r.config.IncusURL, &incus.ConnectionArgs{
		TLSClientCert:      string(certPEM),
		TLSClientKey:       string(keyPEM),
		InsecureSkipVerify: true,
	})
}

// register generates a self-signed ECDSA cert, presents it to Incus over TLS,
// and asks the server to add it to the trust store using the one-time token.
// The server reads the cert from the TLS handshake (see incusd certificates.go),
// applies the restrictions stored in the token metadata, and returns trusted=true.
func (r *Runner) register(certPath, keyPath string) error {
	tokenBytes, err := os.ReadFile(filepath.Join(r.config.SecretsDir, tokenFile))
	if err != nil {
		return fmt.Errorf("reading token: %w", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return errors.New("token file is empty")
	}

	certPEM, keyPEM, err := generateClientCert()
	if err != nil {
		return fmt.Errorf("generating cert: %w", err)
	}

	srv, err := incus.ConnectIncus(r.config.IncusURL, &incus.ConnectionArgs{
		TLSClientCert:      string(certPEM),
		TLSClientKey:       string(keyPEM),
		InsecureSkipVerify: true,
	})
	if err != nil {
		return fmt.Errorf("connecting to register cert: %w", err)
	}

	if err := srv.CreateCertificate(
		incusApi.CertificatesPost{
			CertificatePut: incusApi.CertificatePut{
				Name:       "ic-healthd-" + r.config.Projects[0],
				Restricted: true,
				Projects:   r.config.Projects,
			}, TrustToken: token,
		}); err != nil {
		return fmt.Errorf("registering cert with token: %w", err)
	}

	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return fmt.Errorf("persisting cert: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("persisting key: %w", err)
	}

	if r.config.Debug {
		log.Println("certificate registered and persisted")
	}
	return nil
}

// generateClientCert returns a fresh ECDSA P-384 key pair and self-signed
// X.509 client certificate, both PEM-encoded.
func generateClientCert() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("ecdsa key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("serial: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "ic-healthd"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("creating cert: %w", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshaling key: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
