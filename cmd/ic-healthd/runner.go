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
	"time"

	"github.com/avast/retry-go/v5"
	incus "github.com/lxc/incus/v7/client"
	incusApi "github.com/lxc/incus/v7/shared/api"

	"github.com/lxc/incus-compose/client"
)

// Runner manages all health checkers.
type Runner struct {
	config *Config
	conn   incus.InstanceServer

	running []context.CancelFunc
}

// NewRunner creates a new runner with the given configuration.
func NewRunner(cfg *Config) (*Runner, error) {
	if len(cfg.Projects) == 0 {
		return nil, errors.New("at least one --project is required")
	}

	return &Runner{
		config:  cfg,
		running: []context.CancelFunc{},
	}, nil
}

// Run starts all health checkers and blocks until context is cancelled.
func (r *Runner) Run(ctx context.Context, reload <-chan struct{}) error {
	conn, err := r.connect(ctx)
	if err != nil {
		return fmt.Errorf("connecting to incus: %w", err)
	}
	r.conn = conn.UseProject(r.config.Projects[0])

	slog.Debug("connected to incus", "project", r.config.Projects[0])

	err = r.writeStatus(client.HealthStatusHealthy)
	if err != nil {
		return err
	}

	for {
		r.startCheckers(ctx)
		slog.Info("health daemon running", "instances", len(r.running))

		select {
		case <-ctx.Done():
			return r.writeStatus(client.HealthStatusUnhealthy)
		case <-reload:
			slog.Info("loading additional checkers")
		}
	}
}

func (r *Runner) findHealthd() (string, error) {
	if r.conn == nil {
		return "", client.ErrNotFound
	}

	instances, err := r.conn.GetInstances("")
	if err != nil {
		return "", client.ErrUnknown.Wrap(fmt.Errorf("listing instances: %w", err))
	}

	for _, inst := range instances {
		if inst.Config[client.HealthKeyPrefix+"daemon"] == "true" {
			return inst.Name, nil
		}
	}

	return "", client.ErrNotFound
}

func (r *Runner) writeStatus(status string) error {
	name, err := r.findHealthd()
	if err != nil {
		return fmt.Errorf("finding healthd: %w", err)
	}

	slog.Debug("Writing status", "healthd", name, "status", status)

	inst, etag, err := r.conn.GetInstance(name)
	if err != nil {
		return err
	}

	inst.Config[client.HealthStatusKey] = status
	op, err := r.conn.UpdateInstance(name, inst.Writable(), etag)
	if err != nil {
		return err
	}

	if err := op.Wait(); err != nil {
		return err
	}

	return nil
}

func (r *Runner) startCheckers(ctx context.Context) {
	instances, err := r.discover(r.conn)
	if err != nil {
		slog.Warn("instance discovery had errors", "error", err)
	}

	for _, cancel := range r.running {
		cancel()
	}

	for name, inst := range instances {
		chkCtx, cancel := context.WithCancel(ctx)
		checker := NewChecker(r.conn, name, inst)
		go func() {
			checker.Run(chkCtx, true, false)
		}()

		r.running = append(r.running, cancel)
	}
}

// connect returns an authenticated Incus client.
//
// On first run, the persisted cert is missing: we generate one, register it
// with the one-time TrustToken, and persist it for subsequent runs.
// On restart, the persisted cert is reused and the token (already consumed) is ignored.
func (r *Runner) connect(ctx context.Context) (incus.InstanceServer, error) {
	// Token to register (generates KEY/CERT)
	tokenPath := filepath.Join(r.config.SecretsDir, tokenFile)

	// Paths after r.register(...)
	certDataPath := filepath.Join(r.config.DataDir, certFile)
	keyDataPath := filepath.Join(r.config.DataDir, keyFile)

	if !fileExists(certDataPath) && fileExists(tokenPath) {
		slog.Debug("fresh token performing first-run registration")

		conn, err := retry.NewWithData[incus.InstanceServer](
			retry.Context(ctx),
			retry.Attempts(6),
			retry.Delay(500*time.Millisecond),
		).Do(func() (incus.InstanceServer, error) {
			return r.register(tokenPath)
		})
		if err != nil {
			return nil, fmt.Errorf("first-run registration: %w", err)
		}

		return conn, nil
	} else if !fileExists(keyDataPath) || !fileExists(certDataPath) {
		return nil, fmt.Errorf("no token and no registration happened before")
	}

	slog.Debug("reusing persisted cert from data dir")

	certPEM, err := os.ReadFile(certDataPath)
	if err != nil {
		return nil, fmt.Errorf("reading cert: %w", err)
	}
	keyPEM, err := os.ReadFile(keyDataPath)
	if err != nil {
		return nil, fmt.Errorf("reading key: %w", err)
	}

	return retry.NewWithData[incus.InstanceServer](
		retry.Context(ctx),
		retry.Attempts(6),
		retry.Delay(500*time.Millisecond),
	).Do(func() (incus.InstanceServer, error) {
		return incus.ConnectIncus(r.config.IncusURL, &incus.ConnectionArgs{
			TLSClientCert:      string(certPEM),
			TLSClientKey:       string(keyPEM),
			InsecureSkipVerify: true,
		})
	})
}

// register generates a self-signed ECDSA cert, presents it to Incus over TLS,
// and asks the server to add it to the trust store using the one-time token.
// The server reads the cert from the TLS handshake (see incusd certificates.go),
// applies the restrictions stored in the token metadata, and returns trusted=true.
// The cert/key are persisted to the data dir only after successful registration,
// so a failed attempt is retried on the next run.
func (r *Runner) register(tokenPath string) (incus.InstanceServer, error) {
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		return nil, fmt.Errorf("reading token: %w", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return nil, errors.New("token file is empty")
	}

	certPEM, keyPEM, err := generateClientCert()
	if err != nil {
		return nil, fmt.Errorf("generating cert: %w", err)
	}

	conn, err := incus.ConnectIncus(r.config.IncusURL, &incus.ConnectionArgs{
		TLSClientCert:      string(certPEM),
		TLSClientKey:       string(keyPEM),
		InsecureSkipVerify: true,
	})
	if err != nil {
		return nil, fmt.Errorf("connecting to register cert: %w", err)
	}

	if err := conn.CreateCertificate(
		incusApi.CertificatesPost{
			CertificatePut: incusApi.CertificatePut{
				Name:       "ic-healthd-" + r.config.Projects[0],
				Restricted: true,
				Projects:   r.config.Projects,
			}, TrustToken: token,
		}); err != nil {
		return nil, fmt.Errorf("registering cert with token: %w", err)
	}

	if err := os.MkdirAll(r.config.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating data-dir %v: %w", r.config.DataDir, err)
	}

	keyDataPath := filepath.Join(r.config.DataDir, keyFile)
	if err := os.WriteFile(keyDataPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("saving key %v: %w", keyDataPath, err)
	}

	certDataPath := filepath.Join(r.config.DataDir, certFile)
	if err := os.WriteFile(certDataPath, certPEM, 0o600); err != nil {
		return nil, fmt.Errorf("saving cert %v: %w", certDataPath, err)
	}

	slog.Debug("certificate registered and persisted")
	return conn, nil
}

// discover returns instance configs from the set of healthchecks declared on
// instances in the project the client is scoped to. Instances carrying
// user.healthcheck.daemon=true are skipped (the healthd itself).
// Instances without user.healthcheck.test are skipped (no healthcheck).
// Per-instance parse errors are collected and returned as a joined error;
// valid instance are still registered so one broken instances cannot stop
// the daemon.
func (r *Runner) discover(conn incus.InstanceServer) (map[string]InstanceConfig, error) {
	incusInstances, err := conn.GetInstances(incusApi.InstanceTypeAny)
	if err != nil {
		return nil, fmt.Errorf("listing instances: %w", err)
	}

	instances := make(map[string]InstanceConfig, len(incusInstances))
	var errs error

	for _, inst := range incusInstances {
		if inst.Config[client.HealthKeyPrefix+"daemon"] == "true" {
			continue
		}

		restart := false
		if slices.Contains([]string{"always", "on-failure", "unless-stopped"}, inst.Config["user.restart"]) {
			restart = true
			if inst.Config[client.HealthKeyPrefix+"test"] == "" {
				inst.Config[client.HealthKeyPrefix+"test"] = "[\"NONE\"]"
			}
		}

		if inst.Config[client.HealthKeyPrefix+"test"] == "" && !restart {
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
		RestartDelay:  defaultRestartDelay,
	}

	if err := json.Unmarshal([]byte(cfg[client.HealthKeyPrefix+"test"]), &svc.Test); err != nil {
		return svc, fmt.Errorf("parsing test: %w", err)
	}

	if len(svc.Test) > 0 && svc.Test[0] == "CMD-SHELL" && len(svc.Test) < 2 {
		return svc, errors.New("CMD-SHELL requires a command")
	}

	if v := cfg[client.HealthKeyPrefix+"start_period"]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return svc, fmt.Errorf("parsing start_period: %w", err)
		}
		svc.StartPeriod = d
	}

	if v := cfg[client.HealthKeyPrefix+"start_interval"]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return svc, fmt.Errorf("parsing start_interval: %w", err)
		}
		svc.StartInterval = d
	}

	if v := cfg[client.HealthKeyPrefix+"interval"]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return svc, fmt.Errorf("parsing interval: %w", err)
		}
		svc.Interval = d
	}

	if v := cfg[client.HealthKeyPrefix+"timeout"]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return svc, fmt.Errorf("parsing timeout: %w", err)
		}
		svc.Timeout = d
	}

	if v := cfg[client.HealthKeyPrefix+"retries"]; v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return svc, fmt.Errorf("parsing retries: %w", err)
		}
		if n == 0 {
			return svc, errors.New("retries must be greater than 0")
		}
		svc.Retries = int(n)
	}

	if slices.Contains([]string{"always", "on-failure", "unless-stopped"}, cfg["user.restart"]) {
		svc.Restart = true
	}

	if cfg["user.restart"] == "unless-stopped" {
		svc.UnlessStopped = true
	}

	if svc.Interval.Seconds() > 0 && svc.Retries > 0 {
		svc.RestartDelay = max(
			min(time.Duration(svc.Interval.Milliseconds()*int64(svc.Retries)), maxRestartDelay),
			defaultRestartDelay,
		)
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
