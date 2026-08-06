package main

import (
	"net"
	"testing"

	incusClient "github.com/lxc/incus/v7/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/client"
)

func TestHealthdGatewayIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config map[string]string
		want   string
	}{
		{
			name:   "managed bridge",
			config: map[string]string{"ipv4.address": "10.183.0.1/23"},
			want:   "10.183.0.1",
		},
		{
			name:   "ipv6 only",
			config: map[string]string{"ipv4.address": "none", "ipv6.address": "fd42:4eff:c3c8:344a::1/64"},
			want:   "fd42:4eff:c3c8:344a::1",
		},
		{
			name:   "ipv4 wins over ipv6",
			config: map[string]string{"ipv4.address": "10.137.32.1/24", "ipv6.address": "fd42:4eff:c3c8:344a::1/64"},
			want:   "10.137.32.1",
		},
		{
			name:   "unmanaged bridge has no address",
			config: map[string]string{},
			want:   "",
		},
		{
			name:   "unresolved auto",
			config: map[string]string{"ipv4.address": "auto"},
			want:   "",
		},
		{
			name:   "garbage",
			config: map[string]string{"ipv4.address": "10.0.0.1"},
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ip := healthdGatewayIP(tt.config)
			if tt.want == "" {
				assert.Nil(t, ip)
				return
			}

			require.NotNil(t, ip)
			assert.Equal(t, tt.want, ip.String())
		})
	}
}

// unmanagedNetwork returns the name of a host interface Incus does not manage
// that carries a global address.
func unmanagedNetwork(t *testing.T, conn incusClient.InstanceServer) string {
	t.Helper()

	networks, err := conn.GetNetworks()
	require.NoError(t, err)

	for _, incusNetwork := range networks {
		if incusNetwork.Managed || incusNetwork.Type == "loopback" {
			continue
		}

		if healthdNetworkStateIP(conn, incusNetwork.Name, nil) != nil {
			return incusNetwork.Name
		}
	}

	t.Skip("no unmanaged network with an address on this host")

	return ""
}

func TestHealthdNetworkStateIP(t *testing.T) {
	t.Parallel()
	skipLocal(t)

	c := projectClient(t.Context(), t, "default")
	conn, err := c.GlobalConnection()
	require.NoError(t, err)

	name := unmanagedNetwork(t, conn)

	ip := healthdNetworkStateIP(conn, name, nil)
	require.NotNil(t, ip)
	assert.False(t, ip.IsLoopback())
	assert.False(t, ip.IsLinkLocalUnicast())

	assert.Nil(t, healthdNetworkStateIP(conn, "ic-does-not-exist", nil))

	// An address Incus does not answer on is no use to the sidecar.
	assert.Nil(t, healthdNetworkStateIP(conn, name, map[string]bool{"192.0.2.1": true}))
	assert.Equal(t, ip, healthdNetworkStateIP(conn, name, map[string]bool{ip.String(): true}))
}

// TestHealthdProfileNetwork covers the global sidecar, which takes its nic from
// the default profile. That profile also carries a root disk.
func TestHealthdProfileNetwork(t *testing.T) {
	t.Parallel()
	skipLocal(t)

	c := projectClient(t.Context(), t, "default")
	conn, err := c.GlobalConnection()
	require.NoError(t, err)

	profile, _, err := conn.GetProfile("default")
	require.NoError(t, err)
	require.Contains(t, profile.Devices, "root")

	name, config, err := healthdProfileNetwork(conn)
	require.NoError(t, err)
	assert.NotEqual(t, "root", name)

	if healthdGatewayIP(config) == nil {
		assert.NotNil(t, healthdNetworkStateIP(conn, name, nil))
	}
}

func TestHealthdListenAddrs(t *testing.T) {
	t.Parallel()
	skipLocal(t)

	c := projectClient(t.Context(), t, "default")
	conn, err := c.GlobalConnection()
	require.NoError(t, err)

	server, _, err := conn.GetServer()
	require.NoError(t, err)

	listen := healthdListenAddrs(conn)
	require.Len(t, listen, len(server.Environment.Addresses))

	for _, addr := range server.Environment.Addresses {
		host, _, err := net.SplitHostPort(addr)
		require.NoError(t, err)

		assert.Contains(t, listen, net.ParseIP(host).String())
	}
}

// TestHealthdBridgeIPUnmanagedNetwork covers a sidecar whose bridge is a plain
// host interface, which carries no address in its Incus config.
func TestHealthdBridgeIPUnmanagedNetwork(t *testing.T) {
	t.Parallel()
	skipLocal(t)

	ctx := t.Context()
	c := projectClient(ctx, t, "default")

	conn, err := c.GlobalConnection()
	require.NoError(t, err)

	name := unmanagedNetwork(t, conn)

	netRes, err := c.Resource(client.KindNetwork, name, &client.NetworkConfig{External: true})
	require.NoError(t, err)
	require.NoError(t, client.RunAction(ctx, netRes, client.ActionEnsure))

	network, ok := netRes.(*client.Network)
	require.True(t, ok)
	require.Empty(t, network.IncusNetwork.Config["ipv4.address"])

	ip, got, err := healthdBridgeIP(c, network)
	require.NoError(t, err)
	require.NotNil(t, ip)
	assert.Equal(t, name, got)
	assert.False(t, ip.IsLoopback())

	// The address must be the bridge's own, not something off the listen list,
	// or the sidecar has no route to it.
	state, err := conn.GetNetworkState(name)
	require.NoError(t, err)

	var onBridge []string
	for _, addr := range state.Addresses {
		onBridge = append(onBridge, addr.Address)
	}

	assert.Contains(t, onBridge, ip.String())
	assert.Contains(t, healthdListenAddrs(conn), ip.String())
}
