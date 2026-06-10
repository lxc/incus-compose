package main

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/suite"
)

type E2ESlowSuite struct {
	suite.Suite
	ctx    context.Context
	stdout *bytes.Buffer
	stderr *bytes.Buffer
}

func TestE2ESlowSuite(t *testing.T) {
	if os.Getenv("INCUS_COMPOSE_TEST_LOCAL") != "" {
		t.Skip("Skipping: env INCUS_COMPOSE_TEST_LOCAL is set")
	}

	if os.Getenv("INCUS_COMPOSE_TEST_SLOW") == "" {
		t.Skip("Skipping: env INCUS_COMPOSE_TEST_SLOW is not set")
	}

	suite.Run(t, new(E2ESlowSuite))
}

func (s *E2ESlowSuite) SetupSuite() {
	s.ctx = context.Background()
	s.stdout = &bytes.Buffer{}
	s.stderr = &bytes.Buffer{}
}

func (s *E2ESlowSuite) run(args ...string) error {
	s.stdout.Reset()
	s.stderr.Reset()
	cmd := newRootCommand()
	cmd.Writer = s.stdout
	cmd.ErrWriter = s.stderr
	return cmd.Run(s.ctx, append([]string{"incus-compose"}, args...))
}

func (s *E2ESlowSuite) TestExternalNetwork() {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up test-external-network",
			args:    []string{"-f", "../../test/fixtures/test-external-network/compose.yaml", "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list test-external-network",
			args:    []string{"-f", "../../test/fixtures/test-external-network/compose.yaml", "list"},
			wantErr: false,
		},
	}

	defer func() {
		_ = s.run("-f", "../../test/fixtures/test-external-network/compose.yaml", "down", "--project")
	}()

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

func (s *E2ESlowSuite) TestUpDownGrafana() {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up grafana",
			args:    []string{"-f", "../../test/fixtures/grafana/compose.yaml", "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list grafana",
			args:    []string{"-f", "../../test/fixtures/grafana/compose.yaml", "list"},
			wantErr: false,
		},
	}

	defer func() {
		_ = s.run("-f", "../../test/fixtures/grafana/compose.yaml", "down", "--project")
	}()

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

func (s *E2ESlowSuite) TestUpDownWithIncusOptions() {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up with-incus-options",
			args:    []string{"-f", "../../test/fixtures/with-incus-options/compose.yaml", "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list with-incus-options",
			args:    []string{"-f", "../../test/fixtures/with-incus-options/compose.yaml", "list"},
			wantErr: false,
		},
	}

	defer func() {
		_ = s.run("-f", "../../test/fixtures/with-incus-options/compose.yaml", "down", "--project")
	}()

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

func (s *E2ESlowSuite) TestUpDownWithProjectOptions() {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up with-project-options",
			args:    []string{"-f", "../../test/fixtures/with-project-options/compose.yaml", "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list with-project-options",
			args:    []string{"-f", "../../test/fixtures/with-project-options/compose.yaml", "list"},
			wantErr: false,
		},
	}

	defer func() {
		_ = s.run("-f", "../../test/fixtures/with-project-options/compose.yaml", "down", "--project")
	}()

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

// func (s *E2ESlowSuite) TestUpDownWithNatProxy() {
// 	tests := []struct {
// 		name    string
// 		args    []string
// 		wantErr bool
// 	}{
// 		{
// 			name:    "up with-nat-proxy",
// 			args:    []string{"-f", "../../test/fixtures/with-nat-proxy/compose.yaml", "up", "--detach"},
// 			wantErr: false,
// 		},
// 		{
// 			name:    "list with-nat-proxy",
// 			args:    []string{"-f", "../../test/fixtures/with-nat-proxy/compose.yaml", "list"},
// 			wantErr: false,
// 		},
// 	}

// 	defer func() {
// 		_ = s.run("-f", "../../test/fixtures/with-nat-proxy/compose.yaml", "down", "--project")
// 	}()

// 	for _, tt := range tests {
// 		s.Run(tt.name, func() {
// 			err := s.run(tt.args...)
// 			if tt.wantErr {
// 				s.Error(err)
// 			} else {
// 				s.NoError(err)
// 			}
// 		})
// 	}
// }

func (s *E2ESlowSuite) TestUpDownWithSecrets() {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up with-secrets",
			args:    []string{"-f", "../../test/fixtures/with-secrets/compose.yaml", "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list with-secrets",
			args:    []string{"-f", "../../test/fixtures/with-secrets/compose.yaml", "list"},
			wantErr: false,
		},
	}

	defer func() {
		_ = s.run("-f", "../../test/fixtures/with-secrets/compose.yaml", "down", "--project")
	}()

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

// func (s *E2ESlowSuite) TestUpDownWithTmpfs() {
// 	tests := []struct {
// 		name    string
// 		args    []string
// 		wantErr bool
// 	}{
// 		{
// 			name:    "up with-tmpfs",
// 			args:    []string{"-f", "../../test/fixtures/with-tmpfs/compose.yaml", "up", "--detach"},
// 			wantErr: false,
// 		},
// 		{
// 			name:    "list with-tmpfs",
// 			args:    []string{"-f", "../../test/fixtures/with-tmpfs/compose.yaml", "list"},
// 			wantErr: false,
// 		},
// 		{
// 			name:    "down with-tmpfs",
// 			args:    []string{"-f", "../../test/fixtures/with-tmpfs/compose.yaml", "down", "--project"},
// 			wantErr: false,
// 		},
// 	}

// 	for _, tt := range tests {
// 		s.Run(tt.name, func() {
// 			err := s.run(tt.args...)
// 			if tt.wantErr {
// 				s.Error(err)
// 			} else {
// 				s.NoError(err)
// 			}
// 		})
// 	}
// }

func (s *E2ESlowSuite) TestUpDownWithVolume() {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up with-volume",
			args:    []string{"-f", "../../test/fixtures/with-volume/compose.yaml", "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list with-volume",
			args:    []string{"-f", "../../test/fixtures/with-volume/compose.yaml", "list"},
			wantErr: false,
		},
	}

	defer func() {
		_ = s.run("-f", "../../test/fixtures/with-volume/compose.yaml", "down", "--project")
	}()

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
