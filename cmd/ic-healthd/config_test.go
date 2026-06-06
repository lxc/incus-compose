package main

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type HealthdConfigSuite struct {
	suite.Suite
}

func (s *HealthdConfigSuite) TestDurationJSON() {
	var d Duration

	s.Require().NoError(json.Unmarshal([]byte(`"30s"`), &d))
	s.Equal(30*time.Second, d.Duration())

	encoded, err := json.Marshal(d)
	s.Require().NoError(err)
	s.Equal(`"30s"`, string(encoded))

	s.Error(json.Unmarshal([]byte(`30`), &d))
	s.Error(json.Unmarshal([]byte(`"not-a-duration"`), &d))
}

func (s *HealthdConfigSuite) TestParseServiceConfigDefaults() {
	svc, err := parseServiceConfig(map[string]string{
		"user.healthcheck.test": `["CMD", "curl", "-f", "http://localhost"]`,
	})

	s.Require().NoError(err)
	s.Equal([]string{"CMD", "curl", "-f", "http://localhost"}, svc.Test)
	s.Equal(defaultInterval, svc.Interval.Duration())
	s.Equal(defaultTimeout, svc.Timeout.Duration())
	s.Equal(defaultRetries, svc.Retries)
	s.False(svc.Restart)
}

func (s *HealthdConfigSuite) TestParseServiceConfigCustomValues() {
	svc, err := parseServiceConfig(map[string]string{
		"user.healthcheck.test":     `["CMD-SHELL", "pg_isready"]`,
		"user.healthcheck.interval": "10s",
		"user.healthcheck.timeout":  "2s",
		"user.healthcheck.retries":  "5",
		"user.restart":              "on-failure",
	})

	s.Require().NoError(err)
	s.Equal([]string{"CMD-SHELL", "pg_isready"}, svc.Test)
	s.Equal(10*time.Second, svc.Interval.Duration())
	s.Equal(2*time.Second, svc.Timeout.Duration())
	s.Equal(5, svc.Retries)
	s.True(svc.Restart)
}

func (s *HealthdConfigSuite) TestParseServiceConfigRestartPolicies() {
	for _, policy := range []string{"always", "on-failure", "unless-stopped"} {
		svc, err := parseServiceConfig(map[string]string{
			"user.healthcheck.test": `["NONE"]`,
			"user.restart":          policy,
		})
		s.Require().NoError(err)
		s.True(svc.Restart, "expected restart=true for policy %q", policy)
	}

	svc, err := parseServiceConfig(map[string]string{
		"user.healthcheck.test": `["NONE"]`,
		"user.restart":          "no",
	})
	s.Require().NoError(err)
	s.False(svc.Restart)
}

func (s *HealthdConfigSuite) TestParseServiceConfigErrors() {
	tests := []struct {
		name string
		cfg  map[string]string
	}{
		{name: "test", cfg: map[string]string{"user.healthcheck.test": `not-json`}},
		{name: "interval", cfg: map[string]string{"user.healthcheck.test": `["NONE"]`, "user.healthcheck.interval": "bad"}},
		{name: "timeout", cfg: map[string]string{"user.healthcheck.test": `["NONE"]`, "user.healthcheck.timeout": "bad"}},
		{name: "retries", cfg: map[string]string{"user.healthcheck.test": `["NONE"]`, "user.healthcheck.retries": "bad"}},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			_, err := parseServiceConfig(tt.cfg)
			s.Error(err)
		})
	}
}

func (s *HealthdConfigSuite) TestGenerateClientCert() {
	certPEM, keyPEM, err := generateClientCert()
	s.Require().NoError(err)

	certBlock, _ := pem.Decode(certPEM)
	s.Require().NotNil(certBlock)
	s.Equal("CERTIFICATE", certBlock.Type)

	cert, err := x509.ParseCertificate(certBlock.Bytes)
	s.Require().NoError(err)
	s.Equal("ic-healthd", cert.Subject.CommonName)
	s.Contains(cert.ExtKeyUsage, x509.ExtKeyUsageClientAuth)

	keyBlock, _ := pem.Decode(keyPEM)
	s.Require().NotNil(keyBlock)
	s.Equal("PRIVATE KEY", keyBlock.Type)
	_, err = x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	s.NoError(err)
}

func (s *HealthdConfigSuite) TestFileExists() {
	dir := s.T().TempDir()
	path := filepath.Join(dir, "file")

	s.False(fileExists(path))
	s.Require().NoError(os.WriteFile(path, []byte("ok"), 0o600))
	s.True(fileExists(path))
}

func TestHealthdConfigSuite(t *testing.T) {
	suite.Run(t, new(HealthdConfigSuite))
}
