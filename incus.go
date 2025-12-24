package incuscompose

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	incusClient "github.com/lxc/incus/v6/client"
)

// IncusClientOptions holds configuration for connecting to an Incus server.
type IncusClientOptions struct {
	Options

	// Remote name from Incus CLI config (e.g., "local", "production")
	// If empty, connects to default local Unix socket
	// Ignored if URL is set
	Remote string

	// Direct URL connection (e.g., "https://192.168.1.100:8443")
	// If set, bypasses Remote and Config options
	URL string

	// Accept any certificate (insecure, for testing only)
	// Used with URL connections
	InsecureSkipVerify bool

	// TLS client certificate path for authentication
	// Used with URL connections
	TLSClientCert string

	// TLS client key path for authentication
	// Used with URL connections
	TLSClientKey string

	// Project name to use (overrides remote's default project)
	Project string

	// Path to Incus CLI config directory
	// If empty, uses default (~/.config/incus)
	ConfigDir string
}

func (o *IncusClientOptions) options() *Options {
	return &o.Options
}

// Makes sure it implements `OptionsType`.
var _ (OptionsType) = (*IncusClientOptions)(nil)

// IncusClientOption is a functional option for configuring Incus client connections.
type IncusClientOption func(*IncusClientOptions)

// IncusClientRemote sets the remote name to connect to.
func IncusClientRemote(remote string) IncusClientOption {
	return func(o *IncusClientOptions) {
		o.Remote = remote
	}
}

// IncusClientURL sets the direct URL to connect to (bypasses remote config).
func IncusClientURL(url string) IncusClientOption {
	return func(o *IncusClientOptions) {
		o.URL = url
	}
}

// IncusClientInsecureSkipVerify skips certificate verification (testing only!)
func IncusClientInsecureSkipVerify() IncusClientOption {
	return func(o *IncusClientOptions) {
		o.InsecureSkipVerify = true
	}
}

// IncusClientTLSClientCert sets the path to the TLS client certificate.
func IncusClientTLSClientCert(path string) IncusClientOption {
	return func(o *IncusClientOptions) {
		o.TLSClientCert = path
	}
}

// IncusClientTLSClientKey sets the path to the TLS client key.
func IncusClientTLSClientKey(path string) IncusClientOption {
	return func(o *IncusClientOptions) {
		o.TLSClientKey = path
	}
}

// IncusClientProject sets the project to use.
func IncusClientProject(project string) IncusClientOption {
	return func(o *IncusClientOptions) {
		o.Project = project
	}
}

// IncusClientConfigDir sets the Incus config directory path.
func IncusClientConfigDir(dir string) IncusClientOption {
	return func(o *IncusClientOptions) {
		o.ConfigDir = dir
	}
}

// NewIncusClientOptions creates IncusClientOptions with the given options applied.
func NewIncusClientOptions(opts ...IncusClientOption) IncusClientOptions {
	res := IncusClientOptions{
		Options: Options{
			Verbosity: DefaultVerbosity,
		},
		Remote: "local",
	}

	for _, o := range opts {
		o(&res)
	}

	return res
}

// ConnectIncus connects to an Incus server and returns an InstanceServer client.
func ConnectIncus(ctx context.Context, opts ...IncusClientOption) (*incusClient.ProtocolIncus, error) {
	options := NewIncusClientOptions(opts...)

	args := &incusClient.ConnectionArgs{
		InsecureSkipVerify: options.InsecureSkipVerify,
		AuthType:           "tls",
	}

	// Read TLS client certificate and key files if provided
	if options.TLSClientCert != "" && options.TLSClientKey != "" {
		certPath, err := filepath.Abs(options.TLSClientCert)
		if err != nil {
			return nil, fmt.Errorf("failed to find the TLS client cert '%s': %w", options.TLSClientCert, err)
		}
		keyPath, err := filepath.Abs(options.TLSClientKey)
		if err != nil {
			return nil, fmt.Errorf("failed to find the TLS client key for '%s': %w", options.TLSClientKey, err)
		}

		certData, err := os.ReadFile(certPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read TLS client certificate '%s': %w", certPath, err)
		}

		keyData, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read the TLS client key '%s': %w", keyPath, err)
		}

		args.TLSClientCert = string(certData)
		args.TLSClientKey = string(keyData)
	}

	client, err := incusClient.ConnectIncus(options.URL, args)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Incus URL '%s': %w", options.URL, err)
	}

	// Apply project if specified
	if options.Project != "" {
		client = client.UseProject(options.Project)
	}

	pIncus, ok := client.(*incusClient.ProtocolIncus)
	if !ok {
		return nil, fmt.Errorf("didn't get an incus client for %q", options.URL)
	}
	return pIncus, nil
}
