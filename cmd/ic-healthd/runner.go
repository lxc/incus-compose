package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	incus "github.com/lxc/incus/v7/client"
	incusApi "github.com/lxc/incus/v7/shared/api"
)

// Runner manages all health checkers.
type Runner struct {
	config        *Config
	client        incus.InstanceServer
	knownCheckers []string
}

// NewRunner creates a new runner with the given configuration.
func NewRunner(cfg *Config) (*Runner, error) {
	return &Runner{
		config:        cfg,
		knownCheckers: []string{},
	}, nil
}

// Run starts all health checkers and blocks until context is cancelled.
func (r *Runner) Run(ctx context.Context, reload <-chan struct{}) error {
	client, err := r.connect()
	if err != nil {
		return fmt.Errorf("connecting to incus: %w", err)
	}
	r.client = client.UseProject(r.config.Projects[0])

	slog.Debug("connected to incus", "project", r.config.Projects[0])

	for {
		runCtx, cancel := context.WithCancel(ctx)
		r.startCheckers(runCtx)

		select {
		case <-ctx.Done():
			cancel()
			return nil
		case <-reload:
			slog.Info("loading additional checkers")
		}
	}
}

func (r *Runner) startCheckers(ctx context.Context) {
	instances, err := r.discover(r.client)
	if err != nil {
		slog.Warn("instance discovery had errors", "error", err)
	}

	var wg sync.WaitGroup
	for name, inst := range instances {
		if slices.Contains(r.knownCheckers, name) {
			continue
		}

		checker := NewChecker(r.client, name, inst)
		wg.Add(1)
		go func() {
			defer wg.Done()
			checker.Run(ctx, true, false)
		}()

		r.knownCheckers = append(r.knownCheckers, name)
	}

	slog.Info("health daemon running", "instances", len(instances))

	go func() {
		wg.Wait()
		r.knownCheckers = []string{}
	}()
}

// connect returns an authenticated Incus client.
//
// On first run, the persisted cert is missing: we generate one, register it
// with the one-time TrustToken, and persist it for subsequent runs.
// On restart, the persisted cert is reused and the token (already consumed) is ignored.
func (r *Runner) connect() (incus.InstanceServer, error) {
	// Token to register (generates KEY/CERT)
	tokenPath := filepath.Join(r.config.SecretsDir, tokenFile)

	// Paths after r.register(...)
	certDataPath := filepath.Join(r.config.DataDir, certFile)
	keyDataPath := filepath.Join(r.config.DataDir, keyFile)

	if !fileExists(certDataPath) && fileExists(tokenPath) {
		slog.Debug("fresh token performing first-run registration")

		// Source for register
		certPath := filepath.Join(r.config.SecretsDir, certFile)
		keyPath := filepath.Join(r.config.SecretsDir, keyFile)

		if err := r.register(tokenPath, certPath, keyPath); err != nil {
			return nil, fmt.Errorf("first-run registration: %w", err)
		}
	} else if !fileExists(keyDataPath) || !fileExists(certDataPath) {
		return nil, fmt.Errorf("no token and no registration happened before")
	} else {
		slog.Debug("reusing persisted cert from data dir")
	}

	certPEM, err := os.ReadFile(certDataPath)
	if err != nil {
		return nil, fmt.Errorf("reading cert: %w", err)
	}
	keyPEM, err := os.ReadFile(keyDataPath)
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
func (r *Runner) register(tokenPath, certPath, keyPath string) error {
	tokenBytes, err := os.ReadFile(tokenPath)
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

	// Save key and cert
	if err := os.MkdirAll(r.config.DataDir, 0o700); err != nil {
		return fmt.Errorf("creating data-dir %v: %w", r.config.DataDir, err)
	}

	keySecretsPath := filepath.Join(r.config.DataDir, keyFile)
	if err := os.WriteFile(keySecretsPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("saving cert key %v: %w", keySecretsPath, err)
	}

	certSecretsPath := filepath.Join(r.config.DataDir, certFile)
	if err := os.WriteFile(certSecretsPath, certPEM, 0o600); err != nil {
		return fmt.Errorf("saving cert key %v: %w", certSecretsPath, err)
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

	slog.Debug("certificate registered and persisted")
	return nil
}

// discover returns instance configs from the set of healthchecks declared on
// instances in the project the client is scoped to. Instances carrying
// user.healthcheck.daemon=true are skipped (the healthd itself).
// Instances without user.healthcheck.test are skipped (no healthcheck).
// Per-instance parse errors are collected and returned as a joined error;
// valid instance are still registered so one broken instances cannot stop
// the daemon.
func (r *Runner) discover(client incus.InstanceServer) (map[string]InstanceConfig, error) {
	incusInstances, err := client.GetInstances(incusApi.InstanceTypeAny)
	if err != nil {
		return nil, fmt.Errorf("listing instances: %w", err)
	}

	instances := make(map[string]InstanceConfig, len(incusInstances))
	var errs error

	for _, inst := range incusInstances {
		if inst.Config["user.healthcheck.daemon"] == "true" {
			continue
		}

		restart := false
		if slices.Contains([]string{"always", "on-failure", "on-failure:3", "unless-stopped"}, inst.Config["user.restart"]) {
			restart = true
			if inst.Config["user.healthcheck.test"] == "" {
				inst.Config["user.healthcheck.test"] = "[\"NONE\"]"
			}
		}

		if inst.Config["user.healthcheck.test"] == "" && !restart {
			continue
		}

		instConfig, err := parseInstance(inst.Config)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("%s: %w", inst.Name, err))
			continue
		}

		slog.Debug("Found instance", "instance", inst.Name, "config", instConfig)

		instances[inst.Name] = instConfig
	}

	return instances, errs
}

// parseInstance decodes user.healthcheck.* keys into a InstanceConfig.
// Missing optional keys fall back to sensible defaults.
func parseInstance(cfg map[string]string) (InstanceConfig, error) {
	svc := InstanceConfig{
		StartPeriod:   defaultStartPeriod,
		StartInterval: defaultStartInterval,
		Interval:      defaultInterval,
		Timeout:       defaultTimeout,
		Retries:       defaultRetries,
	}

	if err := json.Unmarshal([]byte(cfg["user.healthcheck.test"]), &svc.Test); err != nil {
		return svc, fmt.Errorf("parsing test: %w", err)
	}

	if v := cfg["user.healthcheck.start_period"]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return svc, fmt.Errorf("parsing start_period: %w", err)
		}
		svc.StartPeriod = d
	}

	if v := cfg["user.healthcheck.start_interval"]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return svc, fmt.Errorf("parsing start_interval: %w", err)
		}
		svc.StartInterval = d
	}

	if v := cfg["user.healthcheck.interval"]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return svc, fmt.Errorf("parsing interval: %w", err)
		}
		svc.Interval = d
	}

	if v := cfg["user.healthcheck.timeout"]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return svc, fmt.Errorf("parsing timeout: %w", err)
		}
		svc.Timeout = d
	}

	if v := cfg["user.healthcheck.retries"]; v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return svc, fmt.Errorf("parsing retries: %w", err)
		}
		svc.Retries = int(n)
	}

	if slices.Contains([]string{"always", "on-failure", "on-failure:3", "unless-stopped"}, cfg["user.restart"]) {
		svc.Restart = true
	}

	if cfg["user.restart"] == "unless-stopped" {
		svc.UnlessStopped = true
	}

	if svc.Interval > 0 && svc.Retries > 0 {
		svc.RestartDelay = time.Duration(svc.Interval.Nanoseconds() * int64(svc.Retries))
	} else {
		svc.RestartDelay = defaultRestartDelay
	}

	return svc, nil
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
