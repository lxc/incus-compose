package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/bradleyjkemp/cupaloy/v2"
	"github.com/lxc/incus/v6/shared/cliconfig"
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

func (s *E2ESuite) defaultProjectClient() *client.Client {
	opts := []client.ClientOption{}
	if url, ok := os.LookupEnv("INCUS_COMPOSE_URL"); ok {
		opts = append(opts, client.ClientURL(url), client.ClientInsecureSkipVerify())
		if cert, ok := os.LookupEnv("INCUS_COMPOSE_CERT"); ok {
			opts = append(opts, client.ClientTLSClientCert(cert))
		}
		if key, ok := os.LookupEnv("INCUS_COMPOSE_KEY"); ok {
			opts = append(opts, client.ClientTLSClientKey(key))
		}
	} else {
		conf, err := cliconfig.LoadConfig("")
		s.Require().NoError(err)

		remote := os.Getenv("INCUS_REMOTE")
		if remote == "" {
			remote = conf.DefaultRemote
		}

		server, err := conf.GetInstanceServer(remote)
		s.Require().NoError(err)
		opts = append(opts, client.ClientProvideInstanceServer(server))
	}

	globalClient := client.New(s.ctx, opts...)
	s.Require().NoError(globalClient.Connect())

	c, err := globalClient.EnsureProject("default")
	s.Require().NoError(err)
	return c
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
			name:    "with-incus-options",
			args:    []string{"-f", "../../test/fixtures/with-incus-options/compose.yaml", "config"},
			wantErr: false,
		},
		{
			name:    "with-incus-options",
			args:    []string{"-f", "../../test/fixtures/with-project-options/compose.yaml", "config"},
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

func (s *E2ESuite) TestDownProjectDeletesNetworks() {
	s.skipIfLocal()

	compose := "../../test/fixtures/simple-nginx/compose.yaml"
	networks := s.plannedNetworkNames(compose)
	s.Require().NotEmpty(networks)

	cleaned := false
	defer func() {
		if !cleaned {
			_ = s.run("-f", compose, "down", "--project")
		}
	}()

	s.Require().NoError(s.run("-f", compose, "up", "--detach", "--pull=missing"))

	c := s.defaultProjectClient()
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
	s.skipIfLocal()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up simple-nginx",
			args:    []string{"-f", "../../test/fixtures/simple-nginx/compose.yaml", "up", "--detach", "--pull=missing"},
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

func (s *E2ESuite) TestLifecycleSimpleNginx() {
	s.skipIfLocal()

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
			args: []string{"-f", compose, "up", "--detach", "--pull=missing"},
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
		{
			name: "restart service",
			args: []string{"-f", compose, "restart", "web"},
		},
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

func (s *E2ESuite) TestExternalNetwork() {
	s.skipIfLocal()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up test-external-network",
			args:    []string{"-f", "../../test/fixtures/test-external-network/compose.yaml", "up", "--detach", "--pull=missing"},
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

func (s *E2ESuite) TestUpDownGrafana() {
	s.skipIfLocal()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up grafana",
			args:    []string{"-f", "../../test/fixtures/grafana/compose.yaml", "up", "--detach", "--pull=missing"},
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

func (s *E2ESuite) TestUpDownScale() {
	s.skipIfLocal()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up nginx-scale",
			args:    []string{"-f", "../../test/fixtures/nginx-scale/compose.yaml", "up", "--detach", "--pull=missing"},
			wantErr: false,
		},
		{
			name:    "scale nginx-scale",
			args:    []string{"-f", "../../test/fixtures/nginx-scale/compose.yaml", "up", "--detach", "--pull=missing", "--scale=web=3"},
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
	s.skipIfLocal()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up nginx-scale",
			args:    []string{"-f", "../../test/fixtures/nginx-scale/compose.yaml", "up", "--detach", "--pull=missing"},
			wantErr: false,
		},
		{
			name:    "downscale nginx-scale",
			args:    []string{"-f", "../../test/fixtures/nginx-scale/compose.yaml", "up", "--detach", "--pull=missing", "--scale=web=6"},
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

func (s *E2ESuite) TestUpDownImmich() {
	s.skipIfLocal()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up immich",
			args:    []string{"-f", "../../test/fixtures/immich/compose.yaml", "up", "--detach", "--pull=missing"},
			wantErr: false,
		},
		{
			name:    "list immich",
			args:    []string{"-f", "../../test/fixtures/immich/compose.yaml", "list"},
			wantErr: false,
		},
	}

	defer func() {
		_ = s.run("-f", "../../test/fixtures/immich/compose.yaml", "down", "--project")
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
	s.skipIfLocal()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up nginx-scale",
			args:    []string{"-f", "../../test/fixtures/nginx-scale/compose.yaml", "up", "--detach", "--pull=missing"},
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

func (s *E2ESuite) TestUpDownWithIncusOptions() {
	s.skipIfLocal()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up with-incus-options",
			args:    []string{"-f", "../../test/fixtures/with-incus-options/compose.yaml", "up", "--detach", "--pull=missing"},
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
	s.skipIfLocal()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up with-project-options",
			args:    []string{"-f", "../../test/fixtures/with-project-options/compose.yaml", "up", "--detach", "--pull=missing"},
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
// 	s.skipIfLocal()

// 	tests := []struct {
// 		name    string
// 		args    []string
// 		wantErr bool
// 	}{
// 		{
// 			name:    "up with-nat-proxy",
// 			args:    []string{"-f", "../../test/fixtures/with-nat-proxy/compose.yaml", "up", "--detach", "--pull=missing"},
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
	s.skipIfLocal()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up with-secrets",
			args:    []string{"-f", "../../test/fixtures/with-secrets/compose.yaml", "up", "--detach", "--pull=missing"},
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

// func (s *E2ESuite) TestUpDownWithTmpfs() {
// 	s.skipIfLocal()

// 	tests := []struct {
// 		name    string
// 		args    []string
// 		wantErr bool
// 	}{
// 		{
// 			name:    "up with-tmpfs",
// 			args:    []string{"-f", "../../test/fixtures/with-tmpfs/compose.yaml", "up", "--detach", "--pull=missing"},
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
	s.skipIfLocal()

	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name:    "up with-volume",
			args:    []string{"-f", "../../test/fixtures/with-volume/compose.yaml", "up", "--detach", "--pull=missing"},
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
	err := s.run("-f", "../../test/fixtures/simple-nginx/compose.yaml", "up", "--detach", "--pull=missing")
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
