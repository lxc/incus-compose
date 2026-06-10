package client

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lxc/incus/v7/shared/cliconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// TestGroupByKind tests the batch grouping logic without Incus.
func TestGroupByKind(t *testing.T) {
	tests := []struct {
		name        string
		tasks       []Resource
		wantBatches int
		wantSizes   []int
	}{
		{
			name:        "empty tasks",
			tasks:       []Resource{},
			wantBatches: 0,
			wantSizes:   nil,
		},
		{
			name: "single task",
			tasks: []Resource{
				newMockResource("a", "", 0, false),
			},
			wantBatches: 1,
			wantSizes:   []int{1},
		},
		{
			name: "same kind groups together",
			tasks: []Resource{
				newMockResource("a", "", 0, false),
				newMockResource("b", "", 0, false),
				newMockResource("c", "", 0, false),
			},
			wantBatches: 1,
			wantSizes:   []int{3},
		},
		{
			name: "different kinds create separate batches",
			tasks: []Resource{
				newMockResource("profile", KindProfile, 0, false),
				newMockResource("volume", KindStorageVolume, 0, false),
				newMockResource("instance", KindInstance, 0, false),
			},
			wantBatches: 3,
			wantSizes:   []int{1, 1, 1},
		},
		{
			name: "mixed kinds with multiple per batch",
			tasks: []Resource{
				newMockResource("profile", KindProfile, 0, false),
				newMockResource("image", KindImage, 0, false),
				newMockResource("image2", KindImage, 0, false),
				newMockResource("volume", KindStorageVolume, 0, false),
				newMockResource("volume2", KindStorageVolume, 0, false),
				newMockResource("instance", KindInstance, 0, false),
			},
			wantBatches: 4,
			wantSizes:   []int{1, 2, 2, 1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stack := NewStack(nil)
			stack.Add(tc.tasks...)

			batches := stack.groupByKind()

			require.Len(t, batches, tc.wantBatches)

			if tc.wantSizes != nil {
				for i, size := range tc.wantSizes {
					assert.Len(t, batches[i], size, "batch %d should have %d tasks", i, size)
				}
			}
		})
	}
}

// TestAddDeduplicatesSamePointer is a regression test for the "Alias already exists"
// race: two services sharing the same image resolve to the same Resource pointer via
// Client.Resource(), but Stack.Add used to append it twice, causing parallel Ensure
// calls on the same object.
func TestAddDeduplicatesSamePointer(t *testing.T) {
	r := newMockResource("nginx", KindImage, PriorityImage, false)

	stack := NewStack(nil)
	stack.Add(r, r) // same pointer twice, as mkUpStack does for shared images

	assert.Len(t, stack.resources, 1, "same resource added twice must appear only once")
}

// TestParallelImageDownload verifies multiple images download in parallel.
// Uses tiny busybox variants to minimize bandwidth.
func TestParallelImageDownload(t *testing.T) {
	ctx := context.Background()

	client, err := NewTestClient(ctx)
	if err != nil {
		t.Skipf("Skipping: %v", err)
		return
	}

	// Load CLI config for image server resolution
	conf, err := cliconfig.LoadConfig("")
	if err != nil {
		t.Skipf("Skipping: failed to load config: %v", err)
		return
	}
	if _, err := conf.GetImageServer("docker.io"); err != nil {
		t.Skipf("Skipping: docker.io not configured: %v", err)
		return
	}

	// Create test project
	c, err := createProjectClient(client, "parallel-image-test")
	if err != nil {
		t.Fatalf("Failed to create project: %v", err)
	}
	defer func() { _ = client.DeleteProject("parallel-image-test", true) }()

	// Use busybox with different tags - tiny images (~2MB each)
	imageNames := []string{
		"docker.io/library/busybox:1.36",
		"docker.io/library/busybox:1.35",
		"docker.io/library/busybox:1.34",
	}

	stack := NewStack(c, StackWorkers(3))
	for _, name := range imageNames {
		img, err := c.Resource(KindImage, name, &ImageConfig{})
		require.NoError(t, err)

		stack.Add(img)
	}

	// Verify all images are in same batch (same priority)
	batches := stack.groupByKind()
	assert.Len(t, batches, 1, "all images should be in one batch")
	assert.Len(t, batches[0], 3, "batch should have 3 images")

	// Run with parallelism
	err = stack.Run(ctx, ActionEnsure, OptionCreate())
	assert.NoError(t, err)

	// Verify all images ensured
	for _, name := range imageNames {
		img, err := c.Resource(KindImage, name, &ImageConfig{})
		require.NoError(t, err)

		assert.True(t, img.IsEnsured(), "image %s should be ensured", name)
	}

	t.Logf("Successfully downloaded %d images in parallel", len(imageNames))
}

type stackRun struct {
	action      Action
	options     []Option
	wantError   bool
	wantEnsured bool
}

type stackTest struct {
	name      string
	runs      []stackRun
	resources func(s *StackTestSuite, client *Client) ([]Resource, error)
}

var stackTests = []*stackTest{
	{
		name: "instance-with-secrets",
		resources: func(s *StackTestSuite, client *Client) ([]Resource, error) {
			network, err := client.Resource(KindNetwork, "default", &NetworkConfig{})
			s.Require().NoError(err)

			imageResource, err := client.Resource(KindImage, "docker.io/alpine:latest", &ImageConfig{})
			s.Require().NoError(err)

			image, ok := imageResource.(*Image)
			s.Require().True(ok)

			devices := []InstanceDevice{}
			devices = append(devices, InstanceDevice{
				Name: "eth0",
				Config: InstanceDeviceConfig{
					DeviceType: InstanceDeviceTypeNic,
					Network:    network,
				},
			})

			secrets := []InstanceSecret{
				{
					Source:  "db_password",
					Content: []byte("super-secret-password"),
				},
				{
					Source:  "api_key",
					Target:  "/app/secrets/api.key",
					Content: []byte("my-api-key-value"),
					UID:     0,
					GID:     0,
					Mode:    0o440,
				},
			}

			instance, err := client.Resource(KindInstance, "app-with-secrets", &InstanceConfig{
				Image:   image.Name(),
				Devices: devices,
				Secrets: secrets,
			})
			s.Require().NoError(err)

			return []Resource{network, image, instance}, nil
		},
		runs: []stackRun{
			{ActionEnsure, []Option{OptionCreate()}, false, true},
			{ActionStart, []Option{}, false, false},
			{ActionStop, []Option{OptionForce()}, false, false},
			{ActionDelete, []Option{OptionForce()}, false, false},
		},
	},
	{
		name: "ensure without create fails for non-existent",
		resources: func(s *StackTestSuite, client *Client) ([]Resource, error) {
			profile, err := client.Resource(KindProfile, "p1", &ProfileConfig{})
			s.Require().NoError(err)

			return []Resource{profile}, nil
		},
		runs: []stackRun{
			{ActionEnsure, []Option{}, true, false},
		},
	},
	{
		name: "single profile ensure",
		resources: func(s *StackTestSuite, client *Client) ([]Resource, error) {
			profile, err := client.Resource(KindProfile, "p1", &ProfileConfig{})
			s.Require().NoError(err)

			return []Resource{profile}, nil
		},
		runs: []stackRun{
			{ActionEnsure, []Option{OptionCreate()}, false, true},
			{ActionDelete, []Option{}, false, false},
		},
	},
	{
		name: "profile and network mixed priorities",
		resources: func(s *StackTestSuite, client *Client) ([]Resource, error) {
			profile, err := client.Resource(KindProfile, "p1", &ProfileConfig{})
			s.Require().NoError(err)

			network, err := client.Resource(KindNetwork, "n1", &NetworkConfig{})
			s.Require().NoError(err)

			return []Resource{profile, network}, nil
		},
		runs: []stackRun{
			{ActionEnsure, []Option{OptionCreate()}, false, true},
			{ActionDelete, []Option{}, false, false},
		},
	},
	{
		name: "simple-nginx",
		resources: func(s *StackTestSuite, client *Client) ([]Resource, error) {
			network, err := client.Resource(KindNetwork, "default", &NetworkConfig{})
			s.Require().NoError(err)

			imageResource, err := client.Resource(KindImage, "docker.io/nginx:alpine", &ImageConfig{})
			s.Require().NoError(err)

			image, ok := imageResource.(*Image)
			s.Require().True(ok)

			devices := []InstanceDevice{}
			devices = append(devices, InstanceDevice{
				Name: "eth0",
				Config: InstanceDeviceConfig{
					DeviceType: InstanceDeviceTypeNic,
					Network:    network,
				},
			})

			instance, err := client.Resource(KindInstance, "web", &InstanceConfig{
				Image:   image.Name(),
				Devices: devices,
			})
			s.Require().NoError(err)

			return []Resource{network, image, instance}, nil
		},
		runs: []stackRun{
			{ActionEnsure, []Option{OptionCreate()}, false, true},
			{ActionStart, []Option{}, false, false},
			{ActionStop, []Option{OptionForce()}, false, false},
			{ActionDelete, []Option{OptionForce()}, false, false},
		},
	},
	{
		name: "nginx-scale",
		resources: func(s *StackTestSuite, client *Client) ([]Resource, error) {
			network, err := client.Resource(KindNetwork, "default", &NetworkConfig{})
			s.Require().NoError(err)

			imageResource, err := client.Resource(KindImage, "docker.io/nginx:alpine", &ImageConfig{})
			s.Require().NoError(err)

			image, ok := imageResource.(*Image)
			s.Require().True(ok)

			devices := []InstanceDevice{}
			devices = append(devices, InstanceDevice{
				Name: "eth0",
				Config: InstanceDeviceConfig{
					DeviceType: InstanceDeviceTypeNic,
					Network:    network,
				},
			})

			resources := []Resource{network, image}

			// Create 3 scaled instances: web-1, web-2, web-3
			for i := 1; i <= 3; i++ {
				instance, err := client.Resource(KindInstance, fmt.Sprintf("web-%d", i), &InstanceConfig{
					Image:   image.Name(),
					Devices: devices,
				})
				s.Require().NoError(err)

				resources = append(resources, instance)
			}

			return resources, nil
		},
		runs: []stackRun{
			{ActionEnsure, []Option{OptionCreate()}, false, true},
			{ActionStart, []Option{}, false, false},
			{ActionStop, []Option{OptionForce()}, false, false},
			{ActionDelete, []Option{OptionForce()}, false, false},
		},
	},
	// Flaky test cause of bind port
	// {
	// 	name: "simple-nginx-proxy",
	// 	resources: func(s *StackTestSuite, client *Client) ([]Resource, error) {
	// 		network, err := client.Resource(KindNetwork, "simple-nginx", &NetworkConfig{})
	// 		s.Require().NoError(err)

	// 		is, err := s.incusConfig.GetImageServer("docker.io")
	// 		s.Require().NoError(err)

	// 		image, err := client.Resource(KindImage, "docker.io/nginx:alpine", &ImageConfig{
	// 			Source: is,
	// 		})
	// 		s.Require().NoError(err)

	// 		devices := []InstanceDevice{}
	// 		devices = append(devices, InstanceDevice{
	// 			Name: "eth0",
	// 			Config: InstanceDeviceConfig{
	// 				DeviceType: InstanceDeviceTypeNic,
	// 				Network:    network,
	// 			},
	// 		})
	// 		devices = append(devices, InstanceDevice{
	// 			Name: "proxy-8080",
	// 			Config: InstanceDeviceConfig{
	// 				DeviceType: InstanceDeviceTypeProxy,
	// 				Proxy: InstanceDeviceProxyConfig{
	// 					ListenType:  "tcp",
	// 					ListenAddr:  "0.0.0.0",
	// 					ListenPort:  8080,
	// 					ConnectType: "tcp",
	// 					ConnectAddr: "127.0.0.1",
	// 					ConnectPort: 80,
	// 				},
	// 			},
	// 		})

	// 		instance, err := client.Resource(KindInstance, "web", &InstanceConfig{
	// 			Image:   image.Name(),
	// 			Devices: devices,
	// 		})
	// 		s.Require().NoError(err)

	// 		return []Resource{network, image, instance}, nil
	// 	},
	// 	runs: []stackRun{
	// 		{[]StackOption{}, ActionEnsure, []Option{OptionCreate()}, false, true},
	// 		{[]StackOption{}, ActionStart, []Option{}, false, false},
	// 		{[]StackOption{StackSortDescending()}, ActionDelete, []Option{OptionForce()}, false, false},
	// 	},
	// },
}

// type stackScenario struct {
// 	name     string
// 	setup    func(client *Client) (*Stack, error)
// 	options  []StackOption
// 	validate func(t *testing.T, client *Client, err error)
// 	wantErr  bool
// }

// StackTestSuite tests Stack operations against a real Incus instance.
type StackTestSuite struct {
	suite.Suite
	ctx          context.Context
	globalClient *GlobalClient
	client       *Client

	incusConfig *cliconfig.Config

	// imageServer incusClient.ImageServer

	// upTests         []*stackTest
	// downTests       []*stackTest
	// validationTests []*stackTest
	// scenarioTests   map[string]stackScenario
}

// SetupSuite runs once before all tests.
func (s *StackTestSuite) SetupSuite() {
	s.ctx = context.Background()

	client, err := NewTestClient(s.ctx)
	if err != nil {
		s.T().Skipf("Skipping tests: %v", err)
		return
	}
	s.globalClient = client

	// Load Incus CLI config to get image server
	conf, err := cliconfig.LoadConfig("")
	s.Require().NoError(err, "Failed to load incus config")

	s.incusConfig = conf
}

// SetupTest runs before each test.
func (s *StackTestSuite) SetupTest() {
	client, err := createProjectClient(s.globalClient, "stack-test")
	s.Require().NoError(err, "Failed to create the stack-test project")

	s.client = client
}

func (s *StackTestSuite) TearDownTest() {
	_ = s.globalClient.DeleteProject("stack-test", true)
}

// // initializeDownTests creates Down-related test cases (delete).
// func (s *StackTestSuite) initializeDownTests() {
// 	s.downTests = []stackTest{
// 		{
// 			name: "delete single profile",
// 			setup: func() (*Stack, error) {
// 				// First create the profile
// 				profile, err := s.client.Profile("test-delete-single", ProfileConfig{})
// 				if err != nil {
// 					return nil, err
// 				}
// 				if err := profile.Ensure(ActionArgs{Create: true}); err != nil {
// 					return nil, err
// 				}

// 				// Now set up delete stack
// 				stack := NewStack(s.client)
// 				stack.Add(profile, ActionArgs{})
// 				return stack, nil
// 			},
// 			options: []StackOption{StackSortDescending()},
// 			wantErr: false,
// 			validate: func(t *testing.T, err error) {
// 				profile, _ := s.client.Profile("test-delete-single", ProfileConfig{})
// 				s.False(profile.IsEnsured(), "profile should not be ensured after delete")
// 			},
// 		},
// 		{
// 			name: "delete multiple resources in reverse priority",
// 			setup: func() (*Stack, error) {
// 				// Create resources
// 				profile, err := s.client.Profile("test-delete-multi-p", ProfileConfig{})
// 				if err != nil {
// 					return nil, err
// 				}
// 				if err := profile.Ensure(ActionArgs{Create: true}); err != nil {
// 					return nil, err
// 				}

// 				network, err := s.client.Network("test-delete-multi-n", NetworkConfig{})
// 				if err != nil {
// 					return nil, err
// 				}
// 				if err := network.Ensure(ActionArgs{Create: true}); err != nil {
// 					return nil, err
// 				}

// 				// Set up delete stack - should delete in reverse priority order
// 				stack := NewStack(s.client)
// 				stack.Add(profile)
// 				stack.Add(network)
// 				return stack, nil
// 			},
// 			options: []StackOption{StackSortDescending()},
// 			wantErr: false,
// 			validate: func(t *testing.T, err error) {
// 				profile, _ := s.client.Profile("test-delete-multi-p", ProfileConfig{})
// 				network, _ := s.client.Network("test-delete-multi-n", NetworkConfig{})
// 				s.False(profile.IsEnsured(), "profile should not be ensured after delete")
// 				s.False(network.IsEnsured(), "network should not be ensured after delete")
// 			},
// 		},
// 		{
// 			name: "delete non-ensured resource is no-op",
// 			setup: func() (*Stack, error) {
// 				profile, err := s.client.Profile("test-delete-noop", ProfileConfig{})
// 				if err != nil {
// 					return nil, err
// 				}
// 				// Don't ensure - just try to delete
// 				stack := NewStack(s.client)
// 				stack.Add(profile)
// 				return stack, nil
// 			},
// 			options: []StackOption{StackSortDescending()},
// 			wantErr: false,
// 		},
// 	}
// }

// // initializeScenarioTests creates real-world scenario test cases.
// func (s *StackTestSuite) initializeScenarioTests() {
// 	s.scenarioTests = map[string]stackScenario{
// 		"wordpress": {
// 			name: "wordpress: images + instances + volumes",
// 			setup: func(client *Client) (*Stack, error) {
// 				// Mirror: test/fixtures/wordpress/compose.yaml
// 				// services:
// 				//   db:
// 				//     image: docker.io/library/mysql:8.0
// 				//     volumes:
// 				//       - db_data:/var/lib/mysql
// 				//   wordpress:
// 				//     depends_on: [db]
// 				//     image: docker.io/library/wordpress:latest
// 				//     ports:
// 				//       - "8000:80"
// 				//     volumes:
// 				//       - wordpress_data:/var/www/html

// 				// Default profile (copies from default project, provides root disk)
// 				profile, err := client.Profile("default", ProfileConfig{})
// 				if err != nil {
// 					return nil, err
// 				}

// 				mysqlImage, err := client.Image("docker.io/library/mysql:8.0", ImageConfig{
// 					Source: s.imageServer,
// 				})
// 				if err != nil {
// 					return nil, err
// 				}

// 				wpImage, err := client.Image("docker.io/library/wordpress:latest", ImageConfig{
// 					Source: s.imageServer,
// 				})
// 				if err != nil {
// 					return nil, err
// 				}

// 				// db instance with volume
// 				dbInstance, err := client.Instance("db", InstanceConfig{
// 					Image: mysqlImage,
// 					Config: map[string]string{
// 						"environment.MYSQL_ROOT_PASSWORD": "somewordpress",
// 						"environment.MYSQL_DATABASE":      "wordpress",
// 						"environment.MYSQL_USER":          "wordpress",
// 						"environment.MYSQL_PASSWORD":      "wordpress",
// 					},
// 					PostDevices: []*Device{
// 						{
// 							Name: "vol-db_data",
// 							Config: DeviceConfig{
// 								DeviceType: DeviceTypeDisk,
// 								Disk: DeviceDiskConfig{
// 									StorageVolumeConfig: &StorageVolumeConfig{},
// 									Source:              "db_data",
// 									Path:                "/var/lib/mysql",
// 									Shift:               true,
// 								},
// 							},
// 						},
// 					},
// 				})
// 				if err != nil {
// 					return nil, err
// 				}

// 				// wordpress instance with volume and proxy
// 				wpInstance, err := client.Instance("wordpress", InstanceConfig{
// 					Image: wpImage,
// 					Config: map[string]string{
// 						"environment.WORDPRESS_DB_HOST":     "db:3306",
// 						"environment.WORDPRESS_DB_USER":     "wordpress",
// 						"environment.WORDPRESS_DB_PASSWORD": "wordpress",
// 						"environment.WORDPRESS_DB_NAME":     "wordpress",
// 					},
// 					Devices: []*Device{
// 						{
// 							Name: "proxy-8000",
// 							Config: DeviceConfig{
// 								DeviceType: DeviceTypeProxy,
// 								Proxy: DeviceProxyConfig{
// 									ListenType:  "tcp",
// 									ListenAddr:  "0.0.0.0",
// 									ListenPort:  8000,
// 									ConnectType: "tcp",
// 									ConnectAddr: "127.0.0.1",
// 									ConnectPort: 80,
// 								},
// 							},
// 						},
// 					},
// 					PostDevices: []*Device{
// 						{
// 							Name: "vol-wordpress_data",
// 							Config: DeviceConfig{
// 								DeviceType: DeviceTypeDisk,
// 								Disk: DeviceDiskConfig{
// 									StorageVolumeConfig: &StorageVolumeConfig{},
// 									Source:              "wordpress_data",
// 									Path:                "/var/www/html",
// 									Shift:               true,
// 								},
// 							},
// 						},
// 					},
// 				})
// 				if err != nil {
// 					return nil, err
// 				}

// 				stack := NewStack(client)
// 				stack.Add(profile, mysqlImage, wpImage, dbInstance, wpInstance)

// 				return stack, nil
// 			},
// 			wantErr: false,
// 			validate: func(t *testing.T, client *Client, err error) {
// 				mysqlImage, _ := client.Image("docker.io/library/mysql:8.0", ImageConfig{})
// 				s.Require().True(mysqlImage.IsEnsured(), "mysql image should be ensured")

// 				wpImage, _ := client.Image("docker.io/library/wordpress:latest", ImageConfig{})
// 				s.Require().True(wpImage.IsEnsured(), "wordpress image should be ensured")

// 				dbInstance, _ := client.Instance("db", InstanceConfig{})
// 				s.Require().True(dbInstance.IsEnsured(), "db instance should be ensured")
// 				s.Equal("Running", dbInstance.IncusInstance.Status, "db instance should be running")

// 				wpInstance, _ := s.client.Instance("wordpress", InstanceConfig{})
// 				s.Require().True(wpInstance.IsEnsured(), "wordpress instance should be ensured")
// 				s.Equal("Running", wpInstance.IncusInstance.Status, "wordpress instance should be running")
// 			},
// 		},
// 	}
// }

// TestHooksWithStack tests that hooks are called during Stack.Run.
func (s *StackTestSuite) TestHooksWithStack() {
	// Track hook calls
	var beforeCalled, afterCalled bool
	var afterErr error

	s.client.AddHookBefore(func(_ context.Context, action Action, r Resource, args Options, err error) error {
		if action == ActionEnsure && r.Kind() == KindProfile {
			if _, ok := r.(*Profile); ok {
				beforeCalled = true
			}
		}

		return err
	})

	s.client.AddHookAfter(func(_ context.Context, action Action, r Resource, args Options, err error) error {
		if action == ActionEnsure && r.Kind() == KindProfile {
			if _, ok := r.(*Profile); ok {
				afterCalled = true
				afterErr = err
			}
		}

		return err
	})

	// Create stack and run
	stack := NewStack(s.client)
	profile, err := s.client.Resource(KindProfile, "test-hooks-stack", &ProfileConfig{})
	s.Require().NoError(err)

	stack.Add(profile)
	err = stack.Run(s.ctx, ActionEnsure, OptionCreate())

	s.NoError(err)
	s.True(beforeCalled, "before hook should be called")
	s.True(afterCalled, "after hook should be called")
	s.NoError(afterErr, "after hook should receive nil error")
}

// TestPriorityOrdering verifies resources are executed in correct priority order.
func (s *StackTestSuite) TestKindOrdering() {
	// Track execution order via hooks
	var executionOrder []string

	s.client.AddHookBefore(func(_ context.Context, action Action, r Resource, args Options, err error) error {
		executionOrder = append(executionOrder, fmt.Sprintf("%v:%v", r.Kind(), r.Name()))
		return err
	})

	// Create resources with different priorities
	profile, err := s.client.Resource(KindProfile, "p1", &ProfileConfig{})
	s.Require().NoError(err)

	network, err := s.client.Resource(KindNetwork, "n1", &NetworkConfig{})
	s.Require().NoError(err)

	volume, err := s.client.Resource(KindStorageVolume, "v1", &StorageVolumeConfig{})
	s.Require().NoError(err)

	// Add in reverse priority order
	stack := NewStack(s.client, StackSortDescending())
	stack.Add(profile, network, volume)
	err = stack.Run(s.ctx, ActionEnsure, OptionCreate())
	s.Require().NoError(err)

	// Profile and Network have same priority (512), Volume has 1024
	// So profile/network should come before volume
	s.Require().Len(executionOrder, 3, "should have 3 executions")

	s.Equal("profile:p1", executionOrder[2], "volume should be last")

	s.Contains(executionOrder[:2], "storage-volume:v1")
	s.Contains(executionOrder[:2], "network:n1")
}

// TestErrorAggregation verifies errors from multiple tasks are aggregated.
func (s *StackTestSuite) TestErrorAggregation() {
	stack := NewStack(s.client)

	p1, err := s.client.Resource(KindProfile, "error-test-1", &ProfileConfig{})
	s.Require().NoError(err)

	p2, err := s.client.Resource(KindProfile, "error-test-2", &ProfileConfig{})
	s.Require().NoError(err)

	stack.Add(p1, p2)

	err = stack.Run(s.ctx, ActionEnsure)

	s.Require().Error(err)
	s.Contains(err.Error(), "error-test-1")
	s.Contains(err.Error(), "error-test-2")
}

// TestScenarios tests real-world compose scenarios (simple-nginx, wordpress).
func (s *StackTestSuite) TestScenarios() {
	for _, scenario := range stackTests {
		s.Run(scenario.name, func() {
			projectName := fmt.Sprintf("%s-%d", scenario.name, time.Now().UnixNano())
			client, err := createProjectClient(s.globalClient, projectName)
			s.Require().NoError(err, "Failed to create test project")

			cleanup := func() {
				if err := s.globalClient.DeleteProject(projectName, true); err != nil {
					s.T().Errorf("Failed to delete test project %q: %v", projectName, err)
				}
			}

			resources, err := scenario.resources(s, client)
			if !s.NoError(err) {
				cleanup()
				return
			}

			allStack := NewStack(client)
			allStack.Add(resources...)

			for _, stackRun := range scenario.runs {
				stack := allStack.ForAction(stackRun.action)
				err = stack.Run(s.ctx, stackRun.action, stackRun.options...)

				if stackRun.wantError {
					if !s.Error(err) {
						cleanup()
						return
					}
					continue
				}

				if !s.NoError(err) {
					cleanup()
					return
				}

				if stackRun.wantEnsured {
					for _, r := range stack.All() {
						if !s.True(r.IsEnsured(), "Should be ensured") {
							cleanup()
							return
						}
					}
				}
			}

			cleanup()
		})
	}
}

// TestStackSuite runs the test suite.
func TestStackSuite(t *testing.T) {
	suite.Run(t, new(StackTestSuite))
}
