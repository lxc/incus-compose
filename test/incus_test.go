package test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/suite"

	"gitlab.com/r3j0/incuscompose"
)

// IncusTestSuite is the test suite for Incus client integration tests.
type IncusTestSuite struct {
	suite.Suite
	ctx  context.Context
	opts []incuscompose.IncusClientOption
}

// SetupSuite runs once before all tests in the suite.
func (s *IncusTestSuite) SetupSuite() {
	// TODO: a hack...
	_, filename, _, _ := runtime.Caller(0)
	projectRoot := filepath.Dir(filepath.Dir(filename))

	s.ctx = context.Background()

	// Check for URL-based connection (preferred for nested testing)
	testURL, ok := os.LookupEnv("INCUS_COMPOSE_URL")
	if !ok {
		s.T().Skip("Skipping Incus integration test - INCUS_COMPOSE_URL is not set.")
		return
	}

	s.opts = []incuscompose.IncusClientOption{
		incuscompose.IncusClientURL(testURL),
		incuscompose.IncusClientInsecureSkipVerify(),
	}

	// Add TLS client certificate if provided
	if cert, ok := os.LookupEnv("INCUS_COMPOSE_CERT"); ok {
		if !filepath.IsAbs(cert) {
			cert = filepath.Join(projectRoot, cert)
		}
		s.opts = append(s.opts, incuscompose.IncusClientTLSClientCert(cert))
	}
	if key, ok := os.LookupEnv("INCUS_COMPOSE_KEY"); ok {
		if !filepath.IsAbs(key) {
			key = filepath.Join(projectRoot, key)
		}
		s.opts = append(s.opts, incuscompose.IncusClientTLSClientKey(key))
	}
}

// TestConnectLocal tests connecting to Incus instance.
func (s *IncusTestSuite) TestConnectLocal() {
	// Try to connect to the test instance
	client, err := incuscompose.ConnectIncus(s.ctx, s.opts...)
	s.Require().NoError(err)

	// Verify we can get server info
	server, _, err := client.GetServer()
	s.Require().NoError(err)

	s.Require().NotNil(server)
}

// TestConnectWithProject tests connecting with a specific project.
func (s *IncusTestSuite) TestConnectWithProject() {
	// Add project override to options
	opts := append(s.opts, incuscompose.IncusClientProject("default"))

	// Try to connect with default project
	client, err := incuscompose.ConnectIncus(s.ctx, opts...)
	s.Require().NoError(err)

	// Verify connection info
	info, err := client.GetConnectionInfo()
	s.Require().NoError(err)

	s.Require().Equal("default", info.Project)
}

// TestIncusSuite runs the test suite.
func TestIncusSuite(t *testing.T) {
	suite.Run(t, new(IncusTestSuite))
}
