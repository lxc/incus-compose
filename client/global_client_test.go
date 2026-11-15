package client

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	incusApi "github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/incustrust"
	"github.com/lxc/incus-compose/shared"
)

func TestEmitProgress(t *testing.T) {
	t.Parallel()

	r := newMockResource("web", KindInstance, 0, true)

	t.Run("forwards to handler", func(t *testing.T) {
		t.Parallel()

		var got Progress
		var gotAction Action
		var gotResource Resource
		gc := &GlobalClient{progressHandler: func(a Action, res Resource, _ Options, p Progress) {
			gotAction, gotResource, got = a, res, p
		}}

		want := Progress{Percent: -1, Text: "Waiting for dependency db"}
		gc.emitProgress(ActionStart, r, Options{}, want)

		require.Equal(t, ActionStart, gotAction)
		require.Same(t, r, gotResource)
		require.Equal(t, want, got)
	})

	t.Run("no handler is a no-op", func(t *testing.T) {
		t.Parallel()

		gc := &GlobalClient{}
		require.NotPanics(t, func() {
			gc.emitProgress(ActionStart, r, Options{}, Progress{Percent: -1, Text: "x"})
		})
	})
}

func TestParsePercent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want int
	}{
		{"native rootfs", "rootfs: 42% (3.10MB/s)", 42},
		{"native complete", "metadata: 100% (876B/s)", 100},
		{"leading zero progress", "rootfs: 0% (0B/s)", 0},
		{"oci status text", "Retrieving OCI image from registry", -1},
		{"oci tarball bytes", "Generating rootfs tarball: 12.3MB (4.1MB/s)", -1},
		{"empty", "", -1},
		{"bare percent sign", "%", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, parsePercent(tt.in))
		})
	}
}

func TestDetectOVN_Requires75Extension(t *testing.T) {
	t.Parallel()

	gc := &GlobalClient{}
	supported, err := gc.DetectOVN()
	require.NoError(t, err)
	require.False(t, supported)
}

// TestDetectOVNMatchesProbe holds the cheap checks to the throwaway OVN network
// they skip: CI runs it against servers with OVN and without.
func TestDetectOVNMatchesProbe(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	ctx := t.Context()

	gc, err := NewTestClient(testContext(t))
	require.NoError(t, err)

	got, err := gc.DetectOVN()
	require.NoError(t, err)

	conn, err := gc.Connection()
	require.NoError(t, err)

	name := fmt.Sprintf("ic-probe-test-%x", time.Now().UnixNano())
	createErr := conn.CreateNetwork(ctx, "default", incusApi.NetworksPost{
		Name:       name,
		Type:       "ovn",
		NetworkPut: incusApi.NetworkPut{Config: map[string]string{"network": "none"}},
	}, nil)
	if createErr == nil {
		require.NoError(t, conn.DeleteNetwork(ctx, "default", name))
	}

	require.Equal(t, createErr == nil, got, "probe error: %v", createErr)
}

// TestConnectRestrictedCertificate is a regression test for #127: a
// project-restricted certificate reads no server config, so core.https_address
// looked unset and Connect refused a server that was listening.
func TestConnectRestrictedCertificate(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	ctx := testContext(t)

	gc, err := NewTestClient(ctx)
	require.NoError(t, err)

	if !gc.IsRemote() {
		t.Skip("a restricted certificate needs an https remote")
	}

	conn, err := gc.Connection()
	require.NoError(t, err)

	info, err := conn.GetConnectionInfo(ctx)
	require.NoError(t, err)

	name := "ic-test-restricted-" + strings.ToLower(shared.RandString(12))
	projects := []string{gc.imageCache.IncusProject()}

	t.Cleanup(func() {
		certs, err := conn.GetCertificates(ctx)
		if err != nil {
			return
		}

		for _, cert := range certs {
			if cert.Name == name {
				_ = conn.DeleteCertificate(ctx, cert.Fingerprint)
			}
		}
	})

	// A token operation never finishes, only its first update carries the token.
	tokenCtx, release := context.WithCancel(ctx)
	updates, err := conn.CreateCertificateToken(tokenCtx, incusApi.CertificatesPost{
		CertificatePut: incusApi.CertificatePut{
			Name:       name,
			Type:       "client",
			Restricted: true,
			Projects:   projects,
		},
		Token: true,
	})
	require.NoError(t, err)

	opAPI, ok := <-updates
	release()
	require.True(t, ok, "the certificate token operation reported nothing")

	token, err := opAPI.ToCertificateAddToken()
	require.NoError(t, err)

	restricted, err := incustrust.Enroll(ctx, incustrust.Config{
		Name:              name,
		URL:               info.URL,
		ServerFingerprint: info.CertificateFingerprint,
		DataDir:           t.TempDir(),
		Restricted:        true,
		Projects:          projects,
	}, token.String())
	require.NoError(t, err)

	rc := New(ctx, ClientProvideConnection(restricted), ClientCacheProject(projects[0]))
	require.NoError(t, rc.Connect())

	_, err = rc.HTTPSAddress()
	require.NoError(t, err)
}
