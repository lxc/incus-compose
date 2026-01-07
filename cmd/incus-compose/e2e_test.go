package main

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/suite"
)

type E2ESuite struct {
	suite.Suite
	ctx    context.Context
	stdout *bytes.Buffer
	stderr *bytes.Buffer
}

func TestE2ESuite(t *testing.T) {
	suite.Run(t, new(E2ESuite))
}

func (s *E2ESuite) SetupSuite() {
	s.ctx = context.Background()
	s.stdout = &bytes.Buffer{}
	s.stderr = &bytes.Buffer{}
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
