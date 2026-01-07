package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/bradleyjkemp/cupaloy/v2"
	"github.com/stretchr/testify/suite"
)

type E2ESuite struct {
	suite.Suite
	ctx         context.Context
	stdout      *bytes.Buffer
	stderr      *bytes.Buffer
	snapshotter *cupaloy.Config
}

func TestE2ESuite(t *testing.T) {
	suite.Run(t, new(E2ESuite))
}

func (s *E2ESuite) SetupSuite() {
	s.ctx = context.Background()
	s.stdout = &bytes.Buffer{}
	s.stderr = &bytes.Buffer{}
	s.snapshotter = cupaloy.New(cupaloy.SnapshotSubdirectory(filepath.Join("..", "..", "test", "snapshots")))
}

func (s *E2ESuite) run(args ...string) error {
	s.stdout.Reset()
	s.stderr.Reset()
	cmd := newRootCommand()
	cmd.Writer = s.stdout
	cmd.ErrWriter = s.stderr
	return cmd.Run(s.ctx, append([]string{"incus-compose"}, args...))
}

func (s *E2ESuite) skipIfLocal() {
	if os.Getenv("INCUS_COMPOSE_TEST_LOCAL") != "" {
		s.T().Skip("Skipping: env INCUS_COMPOSE_TEST_LOCAL is set")
	}
}

func (s *E2ESuite) TestConfigCommand() {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "simple-nginx yaml",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "config"},
			wantErr: false,
		},
		{
			name:    "simple-nginx json",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "config", "--format", "json"},
			wantErr: false,
		},
		{
			name:    "wordpress",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config"},
			wantErr: false,
		},
		{
			name:    "with-secrets",
			args:    []string{"-f", "../../test/fixtures/with-secrets/compose.yaml", "config"},
			wantErr: false,
		},
		{
			name:    "with-restart",
			args:    []string{"-f", "../../test/fixtures/with-restart/compose.yaml", "config"},
			wantErr: false,
		},
		{
			name:    "nonexistent file",
			args:    []string{"-f", "nonexistent.yaml", "config"},
			wantErr: true,
		},
		{
			name:    "invalid yaml",
			args:    []string{"-f", "../../test/fixtures/invalid/compose.yaml", "config"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			err := s.run(tt.args...)
			if tt.wantErr {
				s.Error(err)
			} else {
				s.NoError(err)
			}
		})
	}
}

func (s *E2ESuite) TestUpDown() {
	s.skipIfLocal()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up simple-nginx",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "up"},
			wantErr: false,
		},
		{
			name:    "list simple-nginx",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "list"},
			wantErr: false,
		},
		{
			name:    "down simple-nginx",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "down", "--project"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			err := s.run(tt.args...)
			if tt.wantErr {
				s.Error(err)
			} else {
				s.NoError(err)
			}
		})
	}
}

func (s *E2ESuite) TestUpDownWithScale() {
	s.skipIfLocal()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up nginx-scale",
			args:    []string{"-f", "../../test/fixtures/nginx-scale/compose.yaml", "up"},
			wantErr: false,
		},
		{
			name:    "list nginx-scale",
			args:    []string{"-f", "../../test/fixtures/nginx-scale/compose.yaml", "list"},
			wantErr: false,
		},
		{
			name:    "down nginx-scale",
			args:    []string{"-f", "../../test/fixtures/nginx-scale/compose.yaml", "down", "--project"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			err := s.run(tt.args...)
			if tt.wantErr {
				s.Error(err)
			} else {
				s.NoError(err)
			}
		})
	}
}

func (s *E2ESuite) TestUpDownWithSecrets() {
	s.skipIfLocal()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up with-secrets",
			args:    []string{"-f", "../../test/fixtures/with-secrets/compose.yaml", "up"},
			wantErr: false,
		},
		{
			name:    "list with-secrets",
			args:    []string{"-f", "../../test/fixtures/with-secrets/compose.yaml", "list"},
			wantErr: false,
		},
		{
			name:    "down with-secrets",
			args:    []string{"-f", "../../test/fixtures/with-secrets/compose.yaml", "down", "--project"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			err := s.run(tt.args...)
			if tt.wantErr {
				s.Error(err)
			} else {
				s.NoError(err)
			}
		})
	}
}

func (s *E2ESuite) TestConfigFilterByService() {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "wordpress filter db service",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config", "db"},
			wantErr: false,
		},
		{
			name:    "wordpress filter wordpress service includes db dependency",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config", "wordpress"},
			wantErr: false,
		},
		{
			name:    "config --services list",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config", "--services"},
			wantErr: false,
		},
		{
			name:    "config --volumes list",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config", "--volumes"},
			wantErr: false,
		},
		{
			name:    "config --quiet validation",
			args:    []string{"-f", "../../test/fixtures/wordpress/compose.yaml", "config", "--quiet"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			err := s.run(tt.args...)
			if tt.wantErr {
				s.Error(err)
			} else {
				s.NoError(err)
			}
		})
	}
}

// normalizeListOutput removes dynamic content (IP addresses, network hashes) for snapshot comparison.
func normalizeListOutput(output string) string {
	// Remove IP addresses
	ipRegex := regexp.MustCompile(`\d+\.\d+\.\d+\.\d+`)
	output = ipRegex.ReplaceAllString(output, "")

	// Remove network hash suffix (ic-XXXXXXXXXX -> ic-)
	hashRegex := regexp.MustCompile(`ic-[a-z0-9]{10}`)
	output = hashRegex.ReplaceAllString(output, "ic-")

	return output
}

func (s *E2ESuite) TestListSnapshots() {
	s.skipIfLocal()

	// Setup: create resources
	err := s.run("-f", "../../test/fixtures/simple-nginx/compose.yaml", "up")
	s.Require().NoError(err)

	defer func() {
		_ = s.run("-f", "../../test/fixtures/simple-nginx/compose.yaml", "down", "--project")
	}()

	tests := []struct {
		name string
		args []string
	}{
		{
			name: "list_table",
			args: []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "list"},
		},
		{
			name: "list_yaml",
			args: []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "list", "--format", "yaml"},
		},
		{
			name: "list_json",
			args: []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "list", "--format", "json"},
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			err := s.run(tt.args...)
			s.Require().NoError(err)

			normalized := normalizeListOutput(s.stdout.String())
			s.snapshotter.SnapshotT(s.T(), normalized)
		})
	}
}
