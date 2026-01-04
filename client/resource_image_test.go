package client

// import (
// 	"context"
// 	"testing"

// 	incusClient "github.com/lxc/incus/v6/client"
// 	"github.com/lxc/incus/v6/shared/cliconfig"
// 	"github.com/stretchr/testify/suite"
// )

// // imageTest represents a single image test case.
// type imageTest struct {
// 	name     string
// 	setup    func(client *Client, imageServer incusClient.ImageServer) error
// 	validate func(t *testing.T, client *Client, err error)
// 	wantErr  bool
// }

// // ImageTestSuite tests Image operations against a real Incus instance.
// type ImageTestSuite struct {
// 	suite.Suite
// 	ctx          context.Context
// 	globalClient *GlobalClient
// 	client       *Client
// 	imageServer  incusClient.ImageServer

// 	ensureTests  []imageTest
// 	parsingTests []imageTest
// }

// // SetupSuite runs once before all tests.
// func (s *ImageTestSuite) SetupSuite() {
// 	s.ctx = context.Background()

// 	client, err := NewTestClient(s.ctx, DefaultLogFormat)
// 	if err != nil {
// 		s.T().Skipf("Skipping tests: %v", err)
// 		return
// 	}
// 	s.globalClient = client

// 	// Create a project for image tests
// 	project, err := ensureProjectClient(s.globalClient, "image-test", true)
// 	if err != nil {
// 		s.T().Fatalf("Failed to create test project: %v", err)
// 		return
// 	}
// 	s.client = project

// 	// Load Incus CLI config to get image server
// 	conf, err := cliconfig.LoadConfig("")
// 	if err != nil {
// 		s.T().Skipf("Skipping tests: failed to load Incus config: %v", err)
// 		return
// 	}

// 	// Get docker.io image server
// 	imageServer, err := conf.GetImageServer("docker.io")
// 	if err != nil {
// 		s.T().Skipf("Skipping tests: docker.io not configured: %v", err)
// 		return
// 	}
// 	s.imageServer = imageServer
// }

// // SetupTest runs before each test.
// func (s *ImageTestSuite) SetupTest() {
// 	s.initializeEnsureTests()
// 	s.initializeParsingTests()
// }

// // initializeEnsureTests creates Ensure-related test cases.
// func (s *ImageTestSuite) initializeEnsureTests() {
// 	s.ensureTests = []imageTest{
// 		{
// 			name: "copy image succeeds",
// 			setup: func(client *Client, imageServer incusClient.ImageServer) error {
// 				image, err := client.Image("docker.io/library/alpine:latest", ImageConfig{
// 					Source: imageServer,
// 				})
// 				if err != nil {
// 					return err
// 				}
// 				return image.Ensure(Options{Create: true})
// 			},
// 			wantErr: false,
// 			validate: func(t *testing.T, client *Client, err error) {
// 				image, _ := client.Image("docker.io/library/alpine:latest", ImageConfig{})
// 				s.True(image.IsEnsured())
// 				s.NotNil(image.IncusAlias)
// 				s.Equal("docker.io/library/alpine:latest", image.Name())
// 			},
// 		},
// 		{
// 			name: "ensure without create fails for non-existent",
// 			setup: func(client *Client, imageServer incusClient.ImageServer) error {
// 				image, err := client.Image("docker.io/library/nonexistent-image-12345:latest", ImageConfig{
// 					Source: imageServer,
// 				})
// 				if err != nil {
// 					return err
// 				}
// 				return image.Ensure(Options{Create: false})
// 			},
// 			wantErr: true,
// 			validate: func(t *testing.T, client *Client, err error) {
// 				s.Contains(err.Error(), "not found")
// 			},
// 		},
// 		{
// 			name: "ensure is idempotent",
// 			setup: func(client *Client, imageServer incusClient.ImageServer) error {
// 				image, err := client.Image("docker.io/library/alpine:latest", ImageConfig{
// 					Source: imageServer,
// 				})
// 				if err != nil {
// 					return err
// 				}
// 				// First ensure
// 				if err := image.Ensure(Options{Create: true}); err != nil {
// 					return err
// 				}
// 				// Second ensure should return nil immediately
// 				return image.Ensure(Options{Create: true})
// 			},
// 			wantErr: false,
// 			validate: func(t *testing.T, client *Client, err error) {
// 				image, _ := client.Image("docker.io/library/alpine:latest", ImageConfig{})
// 				s.True(image.IsEnsured())
// 			},
// 		},
// 		{
// 			name: "image returns same instance for same name",
// 			setup: func(client *Client, imageServer incusClient.ImageServer) error {
// 				i1, err := client.Image("docker.io/library/busybox:latest", ImageConfig{
// 					Source: imageServer,
// 				})
// 				if err != nil {
// 					return err
// 				}
// 				i2, err := client.Image("docker.io/library/busybox:latest", ImageConfig{})
// 				if err != nil {
// 					return err
// 				}
// 				s.Same(i1, i2)
// 				return nil
// 			},
// 			wantErr: false,
// 		},
// 		{
// 			name: "ensure without source fails",
// 			setup: func(client *Client, imageServer incusClient.ImageServer) error {
// 				image, err := client.Image("docker.io/library/nginx:alpine", ImageConfig{
// 					// No Source configured
// 				})
// 				if err != nil {
// 					return err
// 				}
// 				return image.Ensure(Options{Create: true})
// 			},
// 			wantErr: true,
// 			validate: func(t *testing.T, client *Client, err error) {
// 				s.Contains(err.Error(), "source not configured")
// 			},
// 		},
// 	}
// }

// // initializeParsingTests creates image reference parsing test cases.
// func (s *ImageTestSuite) initializeParsingTests() {
// 	s.parsingTests = []imageTest{
// 		{
// 			name: "parses full docker reference",
// 			setup: func(client *Client, imageServer incusClient.ImageServer) error {
// 				image, err := client.Image("docker.io/library/alpine:3.18", ImageConfig{})
// 				if err != nil {
// 					return err
// 				}
// 				s.Equal("docker.io", image.Config.Remote)
// 				s.Equal("library/alpine:3.18", image.Config.Image)
// 				return nil
// 			},
// 			wantErr: false,
// 		},
// 		{
// 			name: "parses short docker reference",
// 			setup: func(client *Client, imageServer incusClient.ImageServer) error {
// 				image, err := client.Image("nginx:alpine", ImageConfig{})
// 				if err != nil {
// 					return err
// 				}
// 				s.Equal("docker.io", image.Config.Remote)
// 				s.Equal("library/nginx:alpine", image.Config.Image)
// 				return nil
// 			},
// 			wantErr: false,
// 		},
// 		{
// 			name: "parses ghcr.io reference",
// 			setup: func(client *Client, imageServer incusClient.ImageServer) error {
// 				image, err := client.Image("ghcr.io/someorg/someimage:v1.0", ImageConfig{})
// 				if err != nil {
// 					return err
// 				}
// 				s.Equal("ghcr.io", image.Config.Remote)
// 				s.Equal("someorg/someimage:v1.0", image.Config.Image)
// 				return nil
// 			},
// 			wantErr: false,
// 		},
// 		{
// 			name: "uses provided Remote and Image over parsing",
// 			setup: func(client *Client, imageServer incusClient.ImageServer) error {
// 				image, err := client.Image("some-custom-name", ImageConfig{
// 					Remote: "custom.registry.io",
// 					Image:  "myimage:v2",
// 				})
// 				if err != nil {
// 					return err
// 				}
// 				s.Equal("custom.registry.io", image.Config.Remote)
// 				s.Equal("myimage:v2", image.Config.Image)
// 				return nil
// 			},
// 			wantErr: false,
// 		},
// 	}
// }

// // TestImageTestSuite runs the test suite.
// func TestImageTestSuite(t *testing.T) {
// 	suite.Run(t, new(ImageTestSuite))
// }

// // TestEnsure tests Image.Ensure functionality.
// func (s *ImageTestSuite) TestEnsure() {
// 	for _, tc := range s.ensureTests {
// 		s.Run(tc.name, func() {
// 			var err error
// 			if tc.setup != nil {
// 				err = tc.setup(s.client, s.imageServer)
// 			}

// 			if tc.wantErr {
// 				s.Error(err)
// 			} else {
// 				s.NoError(err)
// 			}

// 			if tc.validate != nil {
// 				tc.validate(s.T(), s.client, err)
// 			}
// 		})
// 	}
// }

// // TestParsing tests image reference parsing.
// func (s *ImageTestSuite) TestParsing() {
// 	// Use a fresh project for parsing tests to avoid conflicts
// 	project, err := ensureProjectClient(s.globalClient, "image-parsing-test", true)
// 	if err != nil {
// 		s.T().Fatalf("Failed to create test project: %v", err)
// 		return
// 	}

// 	for _, tc := range s.parsingTests {
// 		s.Run(tc.name, func() {
// 			var err error
// 			if tc.setup != nil {
// 				err = tc.setup(project, s.imageServer)
// 			}

// 			if tc.wantErr {
// 				s.Error(err)
// 			} else {
// 				s.NoError(err)
// 			}

// 			if tc.validate != nil {
// 				tc.validate(s.T(), project, err)
// 			}
// 		})
// 	}
// }
