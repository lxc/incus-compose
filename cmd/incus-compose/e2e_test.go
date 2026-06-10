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

	"gitlab.com/r3j0/incus-compose/client"
	"gitlab.com/r3j0/incus-compose/project"
)

type E2ESuite struct {
	suite.Suite
	ctx         context.Context
	stdout      *bytes.Buffer
	stderr      *bytes.Buffer
	snapshotter *cupaloy.Config
}

func TestE2ESuite(t *testing.T) {
	if os.Getenv("INCUS_COMPOSE_TEST_LOCAL") != "" {
		t.Skip("Skipping: env INCUS_COMPOSE_TEST_LOCAL is set")
	}

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
	return cmd.Run(s.ctx, append([]string{"incus-compose", "--debug"}, args...))
}

func (s *E2ESuite) plannedNetworkNames(compose string) []string {
	proj, err := project.New().Load(s.ctx, project.LoadFiles([]string{compose}))
	s.Require().NoError(err)

	c := client.NewOfflineClient(s.ctx, proj.Name)
	stack := client.NewStack(c)
	s.Require().NoError(proj.ToStack(c, stack))

	names := []string{}
	for _, r := range stack.All() {
		if r.Kind() == client.KindNetwork {
			names = append(names, r.IncusName())
		}
	}
	return names
}

func (s *E2ESuite) TestDownProjectDeletesNetworks() {
	gc, err := client.NewTestClient(context.Background())
	if err != nil {
		s.T().Skip(err.Error())
	}
	compose := "../../test/fixtures/simple-nginx/compose.yaml"
	networks := s.plannedNetworkNames(compose)
	s.Require().NotEmpty(networks)

	cleaned := false
	defer func() {
		if !cleaned {
			_ = s.run("-f", compose, "down", "--project")
		}
	}()

	c, err := gc.EnsureProject("default")
	s.Require().NoError(err)

	s.Require().NoError(s.run("-f", compose, "up", "--detach"))

	for _, name := range networks {
		_, _, err := c.Connection().GetNetwork(name)
		s.Require().NoError(err)
	}

	s.Require().NoError(s.run("-f", compose, "down", "--project"))
	cleaned = true

	for _, name := range networks {
		_, _, err := c.Connection().GetNetwork(name)
		s.Error(err)
	}
}

func (s *E2ESuite) TestUpDown() {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up simple-nginx",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list simple-nginx",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "list"},
			wantErr: false,
		},
	}

	defer func() {
		_ = s.run("-f", "../../test/fixtures/simple-nginx/compose.yaml", "down", "--project")
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

func (s *E2ESuite) TestUpRecreate() {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up simple-nginx",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "up", "--detach", "--recreate"},
			wantErr: false,
		},
		{
			name:    "list simple-nginx",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "list"},
			wantErr: false,
		},
	}

	defer func() {
		_ = s.run("-f", "../../test/fixtures/simple-nginx/compose.yaml", "down", "--project")
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

func (s *E2ESuite) TestUpUpRecreate() {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up simple-nginx",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list1 simple-nginx",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "list"},
			wantErr: false,
		},
		{
			name:    "up simple-nginx",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "up", "--detach", "--recreate"},
			wantErr: false,
		},
		{
			name:    "list2 simple-nginx",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "list"},
			wantErr: false,
		},
	}

	defer func() {
		_ = s.run("-f", "../../test/fixtures/simple-nginx/compose.yaml", "down", "--project")
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

func (s *E2ESuite) TestUpRecreateDown() {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up simple-nginx",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list simple-nginx",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "list"},
			wantErr: false,
		},
		{
			name:    "recreate simple-nginx",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "up", "--detach", "--recreate"},
			wantErr: false,
		},
		{
			name:    "list recreated simple-nginx",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "list"},
			wantErr: false,
		},
	}

	defer func() {
		_ = s.run("-f", "../../test/fixtures/simple-nginx/compose.yaml", "down", "--project")
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

func (s *E2ESuite) TestLifecycleSimpleNginx() {
	compose := "../../test/fixtures/simple-nginx/compose.yaml"

	defer func() {
		_ = s.run("-f", compose, "down", "--project")
	}()

	tests := []struct {
		name string
		args []string
	}{
		{
			name: "up",
			args: []string{"-f", compose, "up", "--detach"},
		},
		{
			name: "ps table",
			args: []string{"-f", compose, "ps", "--all"},
		},
		{
			name: "ps json",
			args: []string{"-f", compose, "ps", "--all", "--format", "json"},
		},
		{
			name: "ps quiet",
			args: []string{"-f", compose, "ps", "--all", "--quiet"},
		},
		{
			name: "ps services",
			args: []string{"-f", compose, "ps", "--all", "--services"},
		},
		{
			name: "stop service",
			args: []string{"-f", compose, "stop", "web"},
		},
		{
			name: "ps stopped",
			args: []string{"-f", compose, "ps", "--all"},
		},
		{
			name: "start service",
			args: []string{"-f", compose, "start", "web"},
		},
		{
			name: "exec dry run",
			args: []string{"-f", compose, "exec", "--dry-run", "web", "echo", "hello"},
		},
		// {
		// 	name: "restart service",
		// 	args: []string{"-f", compose, "restart", "web"},
		// },
		{
			name: "logs service",
			args: []string{"-f", compose, "logs", "web"},
		},
		{
			name: "down resources",
			args: []string{"-f", compose, "down"},
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.Require().NoError(s.run(tt.args...))
		})
	}
}

func (s *E2ESuite) TestUpDownScale() {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up nginx-scale",
			args:    []string{"-f", "../../test/fixtures/nginx-scale/compose.yaml", "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "scale nginx-scale",
			args:    []string{"-f", "../../test/fixtures/nginx-scale/compose.yaml", "up", "--detach", "--scale=web=3"},
			wantErr: false,
		},
	}

	defer func() {
		_ = s.run("-f", "../../test/fixtures/nginx-scale/compose.yaml", "down", "--project")
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

func (s *E2ESuite) TestUpDownDownscale() {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up nginx-scale",
			args:    []string{"-f", "../../test/fixtures/nginx-scale/compose.yaml", "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "downscale nginx-scale",
			args:    []string{"-f", "../../test/fixtures/nginx-scale/compose.yaml", "up", "--detach", "--scale=web=6"},
			wantErr: false,
		},
	}

	defer func() {
		_ = s.run("-f", "../../test/fixtures/nginx-scale/compose.yaml", "down", "--project")
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

func (s *E2ESuite) TestUpDownWithScale() {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up nginx-scale",
			args:    []string{"-f", "../../test/fixtures/nginx-scale/compose.yaml", "up", "--detach"},
			wantErr: false,
		},
		{
			name:    "list nginx-scale",
			args:    []string{"-f", "../../test/fixtures/nginx-scale/compose.yaml", "list"},
			wantErr: false,
		},
	}

	defer func() {
		_ = s.run("-f", "../../test/fixtures/nginx-scale/compose.yaml", "down", "--project")
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

// normalizeListOutput removes dynamic content (IP addresses, network hashes) for snapshot comparison.
func normalizeListOutput(output string) string {
	// Remove IP addresses
	ipRegex := regexp.MustCompile(`\d+\.\d+\.\d+\.\d+`)
	output = ipRegex.ReplaceAllString(output, "")

	return output
}

func (s *E2ESuite) TestListSnapshots() {
	// Setup: create resources
	err := s.run("-f", "../../test/fixtures/simple-nginx/compose.yaml", "up", "--detach")
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

func (s *E2ESuite) TestExternalNetwork() {
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

func (s *E2ESuite) TestUpDownWithIncusOptions() {
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

func (s *E2ESuite) TestUpDownWithProjectOptions() {
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

// func (s *E2ESuite) TestUpDownWithNatProxy() {
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

func (s *E2ESuite) TestUpDownWithSecrets() {
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

func (s *E2ESuite) TestUpDownWithVolume() {
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
