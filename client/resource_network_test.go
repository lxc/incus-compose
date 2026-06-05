package client

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// ----------------------------------------------------------------------------
// Unit Tests for sanitizeNetworkName
// ----------------------------------------------------------------------------

func TestNetworkNameForProject(t *testing.T) {
	tests := []struct {
		name        string
		projectName string
		prefix      string
		networkName string
		description string
	}{
		{
			name:        "short name no project",
			projectName: "",
			prefix:      "",
			networkName: "web",
			description: "Short names should pass through",
		},
		{
			name:        "short name with project",
			projectName: "test",
			prefix:      "",
			networkName: "web",
			description: "test-web should fit in 13 chars",
		},
		{
			name:        "long name gets hashed",
			projectName: "verylongproject",
			prefix:      "ic-",
			networkName: "verylongnetwork",
			description: "Long names should get ic-{hash10} format",
		},
		{
			name:        "underscore replacement",
			projectName: "my_project",
			prefix:      "",
			networkName: "my_net",
			description: "Underscores should become hyphens",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeNetworkName(tt.projectName, tt.prefix, tt.networkName)

			// All results should fit within maxInterfaceNameLen
			assert.LessOrEqual(t, len(result), maxInterfaceNameLen, "Network name too long")

			// Hash-based names should have the ic- prefix
			if len(tt.projectName+"-"+tt.networkName) > maxInterfaceNameLen && tt.prefix != "" {
				assert.True(t, strings.HasPrefix(result, tt.prefix))
				assert.Len(t, result, len(tt.prefix)+networkNameHashLen) // prefix + 10 char hash
			}

			// Should not contain underscores
			assert.NotContains(t, result, "_")

			// Determinism: same input should produce same output
			result2 := sanitizeNetworkName(tt.projectName, tt.prefix, tt.networkName)
			assert.Equal(t, result, result2, "Network name generation should be deterministic")
		})
	}

	p1n1 := sanitizeNetworkName("p1", "ic-", "n1")
	p2n1 := sanitizeNetworkName("p2", "ic-", "n1")

	assert.NotEqual(t, p1n1, p2n1, "Same name different projects should give different names")
}

func TestNetworkCreateConfig(t *testing.T) {
	tests := []struct {
		name       string
		extensions map[string]string
		want       map[string]string
	}{
		{
			name: "empty",
		},
		{
			name:       "auto addresses are left to incus",
			extensions: map[string]string{"ipv4.address": "auto", "ipv6.address": "auto"},
			want:       map[string]string{"ipv4.address": "auto", "ipv6.address": "auto"},
		},
		{
			name:       "explicit IPv4 gets DHCP range",
			extensions: map[string]string{"ipv4.address": "10.200.0.1/24"},
			want:       map[string]string{"ipv4.address": "10.200.0.1/24", "ipv4.dhcp.ranges": "10.200.0.64-10.200.0.254"},
		},
		{
			name:       "explicit IPv6 gets DHCP range and stateful DHCP",
			extensions: map[string]string{"ipv6.address": "fd42:1::1/64"},
			want: map[string]string{
				"ipv6.address":       "fd42:1::1/64",
				"ipv6.dhcp.ranges":   "fd42:1::100-fd42:1::ffff",
				"ipv6.dhcp.stateful": "true",
			},
		},
		{
			name: "existing ranges are preserved",
			extensions: map[string]string{
				"ipv4.address":     "10.200.0.1/24",
				"ipv4.dhcp.ranges": "10.200.0.100-10.200.0.200",
			},
			want: map[string]string{
				"ipv4.address":     "10.200.0.1/24",
				"ipv4.dhcp.ranges": "10.200.0.100-10.200.0.200",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := networkCreateConfig(tt.extensions)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDNSmasqRecords(t *testing.T) {
	tests := []struct {
		name       string
		serviceIPs map[string][]string
		expected   string
	}{
		{
			name:       "empty",
			serviceIPs: map[string][]string{},
			expected:   "",
		},
		{
			name:       "single service single ip",
			serviceIPs: map[string][]string{"database": {"10.0.0.2"}},
			expected:   "address=/database/10.0.0.2\n",
		},
		{
			name:       "single service round-robin",
			serviceIPs: map[string][]string{"web": {"10.0.0.3", "10.0.0.4"}},
			expected:   "address=/web/10.0.0.3\naddress=/web/10.0.0.4\n",
		},
		{
			name:       "multiple services sorted",
			serviceIPs: map[string][]string{"web": {"10.0.0.3"}, "app": {"10.0.0.5"}},
			expected:   "address=/app/10.0.0.5\naddress=/web/10.0.0.3\n",
		},
		{
			name:       "dual stack",
			serviceIPs: map[string][]string{"db": {"10.0.0.2", "fd42::2"}},
			expected:   "address=/db/10.0.0.2\naddress=/db/fd42::2\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, dnsmasqRecords(tt.serviceIPs))
		})
	}
}

// ----------------------------------------------------------------------------
// candidateNames Unit Tests (offline, no Incus required)
// ----------------------------------------------------------------------------

func TestCandidateNames_WithOverride(t *testing.T) {
	c := NewOfflineClient(context.Background(), "myproject")
	r, err := c.Resource(KindNetwork, "shared", &NetworkConfig{
		External:     true,
		OverrideName: "my-production-net",
	})
	require.NoError(t, err)
	net, ok := r.(*Network)
	require.True(t, ok)

	candidates := net.candidateNames()
	require.Len(t, candidates, 4)
	assert.Equal(t, "my-production-net", candidates[0], "override raw first")
	assert.True(t, strings.HasPrefix(candidates[1], "ic-"), "override sanitized second")
	assert.Equal(t, "shared", candidates[2], "compose name raw third")
	assert.True(t, strings.HasPrefix(candidates[3], "ic-"), "compose name sanitized fourth")
}

func TestCandidateNames_WithoutOverride(t *testing.T) {
	c := NewOfflineClient(context.Background(), "myproject")
	r, err := c.Resource(KindNetwork, "shared", &NetworkConfig{External: true})
	require.NoError(t, err)
	net, ok := r.(*Network)
	require.True(t, ok)

	candidates := net.candidateNames()
	require.Len(t, candidates, 2)
	assert.Equal(t, "shared", candidates[0], "compose name raw first")
	assert.True(t, strings.HasPrefix(candidates[1], "ic-"), "compose name sanitized second")
}

func TestCandidateNames_DeduplicatesShortName(t *testing.T) {
	// With empty project name, sanitize(name) == name for short names.
	// The raw and sanitized candidates collapse into one entry.
	c := NewOfflineClient(context.Background(), "")
	r, err := c.Resource(KindNetwork, "mynet", &NetworkConfig{External: true})
	require.NoError(t, err)
	net, ok := r.(*Network)
	require.True(t, ok)

	candidates := net.candidateNames()
	require.Len(t, candidates, 1, "raw and sanitized are the same — should deduplicate")
	assert.Equal(t, "mynet", candidates[0])
}

// ----------------------------------------------------------------------------
// Integration Tests
// ----------------------------------------------------------------------------

// NetworkSuite tests Network operations against a real Incus instance.
type NetworkSuite struct {
	suite.Suite
	ctx          context.Context
	globalClient *GlobalClient
	client       *Client
	projectName  string
}

// SetupSuite runs once before all tests.
func (s *NetworkSuite) SetupSuite() {
	s.ctx = context.Background()
	s.projectName = "network-test"

	gc, err := NewTestClient(s.ctx)
	if err != nil {
		s.T().Skipf("Skipping tests: %v", err)
		return
	}
	s.globalClient = gc
}

// SetupTest runs before each test - creates fresh project.
func (s *NetworkSuite) SetupTest() {
	client, err := createProjectClient(s.globalClient, s.projectName)
	if err != nil {
		s.T().Fatalf("Failed to create test project: %v", err)
	}
	s.client = client
}

// TearDownTest runs after each test - cleans up project.
func (s *NetworkSuite) TearDownTest() {
	if err := s.globalClient.DeleteProject(s.projectName, true); err != nil {
		s.T().Fatalf("Failed to delete the project after run: %v", err)
	}
}

// ----------------------------------------------------------------------------
// Ensure Tests
// ----------------------------------------------------------------------------

func (s *NetworkSuite) TestEnsure_WithCreate() {
	r, err := s.client.Resource(KindNetwork, "test-net", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())
}

func (s *NetworkSuite) TestEnsure_WithoutCreate_Fails() {
	r, err := s.client.Resource(KindNetwork, "non-existent", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure)
	s.Require().Error(err)
	s.False(r.IsEnsured())
	s.ErrorIs(err, ErrNotFound)
}

func (s *NetworkSuite) TestEnsure_Idempotent() {
	r, err := s.client.Resource(KindNetwork, "test-idempotent", &NetworkConfig{})
	s.Require().NoError(err)

	// First ensure
	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())

	// Second ensure - should return immediately
	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())
}

func (s *NetworkSuite) TestEnsure_WithoutCreate_ThenWithCreate() {
	r, err := s.client.Resource(KindNetwork, "test-retry", &NetworkConfig{})
	s.Require().NoError(err)

	// First attempt without create - fails
	err = RunAction(r, ActionEnsure)
	s.Require().Error(err)
	s.False(r.IsEnsured())

	// Second attempt with create - succeeds
	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())
}

func (s *NetworkSuite) TestEnsure_DefaultType() {
	r, err := s.client.Resource(KindNetwork, "test-default-type", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)

	network, ok := r.(*Network)
	s.Require().True(ok)
	s.NotNil(network.IncusNetwork)
	s.Equal("bridge", network.IncusNetwork.Type)
}

func (s *NetworkSuite) TestEnsure_CustomType() {
	r, err := s.client.Resource(KindNetwork, "test-custom-type", &NetworkConfig{
		Type: "bridge",
	})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)

	network, ok := r.(*Network)
	s.Require().True(ok)
	s.Equal("bridge", network.IncusNetwork.Type)
}

// ----------------------------------------------------------------------------
// ResourceStore Tests
// ----------------------------------------------------------------------------

func (s *NetworkSuite) TestResource_ReturnsSameInstance() {
	r1, err := s.client.Resource(KindNetwork, "test-same", &NetworkConfig{})
	s.Require().NoError(err)

	r2, err := s.client.Resource(KindNetwork, "test-same", &NetworkConfig{})
	s.Require().NoError(err)

	s.Same(r1, r2)
}

func (s *NetworkSuite) TestResource_DifferentNamesAreDifferent() {
	r1, err := s.client.Resource(KindNetwork, "network-a", &NetworkConfig{})
	s.Require().NoError(err)

	r2, err := s.client.Resource(KindNetwork, "network-b", &NetworkConfig{})
	s.Require().NoError(err)

	s.NotSame(r1, r2)
}

// ----------------------------------------------------------------------------
// Delete Tests
// ----------------------------------------------------------------------------

func (s *NetworkSuite) TestDelete_AfterEnsure() {
	r, err := s.client.Resource(KindNetwork, "test-delete", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())

	err = RunAction(r, ActionDelete, OptionForce())
	s.Require().NoError(err)
	s.False(r.IsEnsured())
}

func (s *NetworkSuite) TestDelete_NotEnsured_NoError() {
	r, err := s.client.Resource(KindNetwork, "never-created", &NetworkConfig{})
	s.Require().NoError(err)

	// Delete without ensure should not error
	err = RunAction(r, ActionDelete)
	s.Require().NoError(err)
}

// ----------------------------------------------------------------------------
// Hook Tests
// ----------------------------------------------------------------------------

func (s *NetworkSuite) TestHook_BeforeIsCalled() {
	called := false
	s.client.AddHookBefore(func(action Action, r Resource, args Options, err error) error {
		if action == ActionEnsure && r.Kind() == KindNetwork {
			called = true
		}
		return err
	})

	r, err := s.client.Resource(KindNetwork, "test-before-hook", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(called, "before hook should have been called")
}

func (s *NetworkSuite) TestHook_AfterIsCalled() {
	called := false
	s.client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
		if action == ActionEnsure && r.Kind() == KindNetwork {
			called = true
		}
		return err
	})

	r, err := s.client.Resource(KindNetwork, "test-after-hook", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(called, "after hook should have been called")
}

func (s *NetworkSuite) TestHook_AfterReceivesError() {
	var receivedErr error
	s.client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
		if action == ActionEnsure && r.Kind() == KindNetwork {
			receivedErr = err
		}
		return err
	})

	r, err := s.client.Resource(KindNetwork, "non-existent", &NetworkConfig{})
	s.Require().NoError(err)

	_ = RunAction(r, ActionEnsure) // without create, will fail
	s.NotNil(receivedErr, "after hook should receive the error")
}

func (s *NetworkSuite) TestHook_BeforeCanAbort() {
	s.client.AddHookBefore(func(action Action, r Resource, args Options, err error) error {
		if r.Name() == "abort-me" {
			return ErrAborted
		}
		return err
	})

	r, err := s.client.Resource(KindNetwork, "abort-me", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.ErrorIs(err, ErrAborted)
	s.False(r.IsEnsured())
}

func (s *NetworkSuite) TestHook_AfterCanModifyError() {
	s.client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
		if err != nil {
			return ErrAborted // replace error
		}
		return nil
	})

	r, err := s.client.Resource(KindNetwork, "non-existent", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure) // will fail, hook replaces error
	s.ErrorIs(err, ErrAborted)
}

func (s *NetworkSuite) TestHook_DeleteAction() {
	var action Action
	s.client.AddHookBefore(func(a Action, r Resource, args Options, err error) error {
		action = a
		return err
	})

	r, err := s.client.Resource(KindNetwork, "test-delete-hook", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.Equal(ActionEnsure, action)

	err = RunAction(r, ActionDelete)
	s.Require().NoError(err)
	s.Equal(ActionDelete, action)
}

// ----------------------------------------------------------------------------
// New Client Tests (persistence)
// ----------------------------------------------------------------------------

func (s *NetworkSuite) TestEnsure_ExistsOnNewClient() {
	// Create network
	r, err := s.client.Resource(KindNetwork, "test-persist", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)

	// Get new client for same project
	newClient, err := s.globalClient.getProject(s.projectName)
	s.Require().NoError(err)

	// Ensure without create should find it
	r2, err := newClient.Resource(KindNetwork, "test-persist", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r2, ActionEnsure) // no create
	s.Require().NoError(err)
	s.True(r2.IsEnsured())
}

// ----------------------------------------------------------------------------
// IncusName Tests
// ----------------------------------------------------------------------------

func (s *NetworkSuite) TestIncusName_ShortName() {
	r, err := s.client.Resource(KindNetwork, "web", &NetworkConfig{})
	s.Require().NoError(err)

	network, ok := r.(*Network)
	s.Require().True(ok)
	// Project is "network-test", so full name would be "network-test-web" (15 chars)
	// This exceeds 13 chars, so it should be hashed
	s.LessOrEqual(len(network.IncusName()), maxInterfaceNameLen)
}

func (s *NetworkSuite) TestIncusName_LongNameHashed() {
	r, err := s.client.Resource(KindNetwork, "very-long-network-name", &NetworkConfig{})
	s.Require().NoError(err)

	network, ok := r.(*Network)
	s.Require().True(ok)
	s.LessOrEqual(len(network.IncusName()), maxInterfaceNameLen)
	s.True(strings.HasPrefix(network.IncusName(), "ic-"))
}

func (s *NetworkSuite) TestIncusName_Deterministic() {
	r1, err := s.client.Resource(KindNetwork, "det-test", &NetworkConfig{})
	s.Require().NoError(err)

	r2, err := s.client.Resource(KindNetwork, "det-test", &NetworkConfig{})
	s.Require().NoError(err)

	network1, ok := r1.(*Network)
	s.Require().True(ok)
	network2, ok := r2.(*Network)
	s.Require().True(ok)
	s.Equal(network1.IncusName(), network2.IncusName())
}

// ----------------------------------------------------------------------------
// External Network Tests
// ----------------------------------------------------------------------------

func (s *NetworkSuite) TestExternal_InitialIncusNameIsRaw() {
	// Without an OverrideName the static initial guess is the raw compose name.
	// The real Incus name is confirmed via candidateNames() during Ensure.
	r, err := s.client.Resource(KindNetwork, "incusbr0", &NetworkConfig{External: true})
	s.Require().NoError(err)

	network, ok := r.(*Network)
	s.Require().True(ok)
	s.Equal("incusbr0", network.IncusName())
}

func (s *NetworkSuite) TestExternal_Incusbr0Resolves() {
	// incusbr0 is the default Incus bridge present on all installations.
	// It must be found via the raw compose-name candidate.
	r, err := s.client.Resource(KindNetwork, "incusbr0", &NetworkConfig{External: true})
	s.Require().NoError(err)

	s.Require().NoError(RunAction(r, ActionEnsure))
	s.True(r.IsEnsured())

	network, ok := r.(*Network)
	s.Require().True(ok)
	s.Equal("incusbr0", network.IncusName())
}

func (s *NetworkSuite) TestExternal_EnsureFailsIfNotExists() {
	r, err := s.client.Resource(KindNetwork, "non-existent-external", &NetworkConfig{External: true})
	s.Require().NoError(err)

	// External network ensure should fail if network doesn't exist
	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().Error(err)
	s.ErrorIs(err, ErrNotFound)
	s.False(r.IsEnsured())
}

func (s *NetworkSuite) TestExternal_DeleteIsNoOp() {
	// First create a real network to test with
	r, err := s.client.Resource(KindNetwork, "test-ext-del", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)
	s.True(r.IsEnsured())

	network, ok := r.(*Network)
	s.Require().True(ok)
	incusName := network.IncusName()

	// Now create an external reference to it
	extR, err := s.client.Resource(KindNetwork, incusName, &NetworkConfig{External: true})
	s.Require().NoError(err)

	err = RunAction(extR, ActionEnsure)
	s.Require().NoError(err)
	s.True(extR.IsEnsured())

	// Delete the external reference - should be no-op
	err = RunAction(extR, ActionDelete)
	s.Require().NoError(err)

	// Network should still exist (verify with original resource)
	newClient, err := s.globalClient.getProject(s.projectName)
	s.Require().NoError(err)

	checkR, err := newClient.Resource(KindNetwork, "test-ext-del", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(checkR, ActionEnsure)
	s.Require().NoError(err)
	s.True(checkR.IsEnsured())
}

// ----------------------------------------------------------------------------
// DNS Alias Tests
// ----------------------------------------------------------------------------

func (s *NetworkSuite) TestUpdateDNSAliases_SetsRawDnsmasq() {
	r, err := s.client.Resource(KindNetwork, "test-dns", &NetworkConfig{})
	s.Require().NoError(err)

	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)

	network, ok := r.(*Network)
	s.Require().True(ok)

	err = network.UpdateDNSAliases(map[string][]string{"database": {"10.0.0.2"}})
	s.Require().NoError(err)

	// Re-fetch from a fresh client to confirm it persisted.
	newClient, err := s.globalClient.getProject(s.projectName)
	s.Require().NoError(err)

	check, err := newClient.Resource(KindNetwork, "test-dns", &NetworkConfig{})
	s.Require().NoError(err)
	err = RunAction(check, ActionEnsure)
	s.Require().NoError(err)

	checkNet, ok := check.(*Network)
	s.Require().True(ok)
	s.Equal("address=/database/10.0.0.2\n", checkNet.IncusNetwork.Config["raw.dnsmasq"])
}

func (s *NetworkSuite) TestUpdateDNSAliases_ExternalIsNoOp() {
	r, err := s.client.Resource(KindNetwork, "test-ext-dns", &NetworkConfig{})
	s.Require().NoError(err)
	err = RunAction(r, ActionEnsure, OptionCreate())
	s.Require().NoError(err)

	network, ok := r.(*Network)
	s.Require().True(ok)
	incusName := network.IncusName()

	extR, err := s.client.Resource(KindNetwork, incusName, &NetworkConfig{External: true})
	s.Require().NoError(err)
	err = RunAction(extR, ActionEnsure)
	s.Require().NoError(err)

	extNet, ok := extR.(*Network)
	s.Require().True(ok)

	// External networks are not managed - update must be a no-op.
	err = extNet.UpdateDNSAliases(map[string][]string{"database": {"10.0.0.2"}})
	s.Require().NoError(err)
	s.Empty(extNet.IncusNetwork.Config["raw.dnsmasq"])
}

// ----------------------------------------------------------------------------
// Run the suite
// ----------------------------------------------------------------------------

func TestNetworkSuite(t *testing.T) {
	suite.Run(t, new(NetworkSuite))
}

func TestCalcIPv4DHCPRange(t *testing.T) {
	tests := []struct {
		name    string
		cidr    string
		want    string
		wantErr bool
	}{
		{
			name: "/24 bridge address",
			cidr: "10.100.0.1/24",
			// hostBits=8, staticEnd=1<<6=64, lastUsable=254
			want: "10.100.0.64-10.100.0.254",
		},
		{
			name: "/24 normalized network address",
			cidr: "10.100.0.0/24",
			want: "10.100.0.64-10.100.0.254",
		},
		{
			name: "/16",
			cidr: "172.16.0.1/16",
			// hostBits=16, staticEnd=1<<14=16384=0x4000 → .64.0, lastUsable=65534
			want: "172.16.64.0-172.16.255.254",
		},
		{
			name: "/28 (small subnet)",
			cidr: "192.168.1.1/28",
			// hostBits=4, staticEnd=1<<2=4, lastUsable=14
			want: "192.168.1.4-192.168.1.14",
		},
		{
			name:    "invalid CIDR",
			cidr:    "not-a-cidr",
			wantErr: true,
		},
		{
			name:    "/31 too small",
			cidr:    "10.0.0.0/31",
			wantErr: true,
		},
		{
			name:    "/32 too small",
			cidr:    "10.0.0.1/32",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := calcIPv4DHCPRange(tt.cidr)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCalcIPv6DHCPRange(t *testing.T) {
	tests := []struct {
		name    string
		cidr    string
		want    string
		wantErr bool
	}{
		{
			name: "/64 bridge address",
			cidr: "fd42:abc::1/64",
			// static: ::0-::ff, DHCP: ::100-::ffff
			want: "fd42:abc::100-fd42:abc::ffff",
		},
		{
			name: "/64 normalized network address",
			cidr: "fd42:abc::/64",
			want: "fd42:abc::100-fd42:abc::ffff",
		},
		{
			name: "different prefix",
			cidr: "fd00:1234:5678::1/64",
			want: "fd00:1234:5678::100-fd00:1234:5678::ffff",
		},
		{
			name:    "invalid CIDR",
			cidr:    "not-a-cidr",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := calcIPv6DHCPRange(tt.cidr)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
