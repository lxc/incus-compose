package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	incusApi "github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/incustrust"
	"github.com/lxc/incus-compose/internal/testlib"
	"github.com/lxc/incus-compose/shared"
)

// TestNoDanglingNetworksAfterDown is a regression test for the project default
// network not being removed after `down --project`.
func TestNoDanglingNetworksAfterDown(t *testing.T) {
	t.Parallel()
	testlib.SkipLocal(t)

	ctx := t.Context()
	pn := t.Name()
	compose := testlib.Fixture(t, "simple", "compose.yaml")

	testlib.CleanupCompose(t, pn, "-f", compose, "down", "--project")

	_, err := testlib.RunCompose(ctx, t, pn, "", nil, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	_, err = testlib.RunCompose(ctx, t, pn, "", nil, "-f", compose, "down", "--project")
	require.NoError(t, err)

	gc, err := client.NewTestClient(ctx)
	require.NoError(t, err)

	conn, err := gc.Connection()
	require.NoError(t, err)

	networkName := client.SanitizeNetworkName(pn, "ic-", "default")
	networkNames, err := conn.GetNetworkNames(t.Context(), incusApi.ProjectDefaultName)
	require.NoError(t, err)

	require.NotContains(t, networkNames, networkName, "network %q was not removed by down --project", networkName)
}

// TestE2EStartStopIdempotent checks that running start/stop twice (idempotent) works without errors.
func TestE2EStartStopIdempotent(t *testing.T) {
	t.Parallel()
	testlib.SkipE2E(t)

	compose := testlib.Fixture(t, "simple", "compose.yaml")

	ctx := t.Context()
	pn := t.Name()

	testlib.CleanupCompose(t, pn, "-f", compose, "down", "--project")

	tests := []e2eTest{
		{
			name: "up",
			args: []string{"-f", compose, "up", "--detach"},
		},
		{
			name: "stop",
			args: []string{"-f", compose, "stop"},
		},
		{
			name: "stop idempotent",
			args: []string{"-f", compose, "stop"},
		},
		{
			name: "start",
			args: []string{"-f", compose, "start"},
		},
		{
			name: "start idempotent",
			args: []string{"-f", compose, "start"},
		},
	}

	for _, tt := range tests {
		_, err := testlib.RunCompose(ctx, t, pn, "", nil, tt.args...)
		require.NoError(t, err)
	}
}

func TestE2ENoImageCache(t *testing.T) {
	t.Parallel()
	testlib.SkipE2E(t)

	compose := testlib.Fixture(t, "simple", "compose.yaml")

	ctx := t.Context()
	pn := t.Name()

	testlib.CleanupCompose(t, pn, "-f", compose, "down", "--project")

	_, err := testlib.RunCompose(ctx, t, pn, "", nil, "-f", compose, "up", "--detach", "--image-cache", "")
	require.NoError(t, err)
}

// TestE2EUpRecreateDependents pins that `up --recreate` on a service brings back
// services that depend on it (#192).
func TestE2EUpRecreateDependents(t *testing.T) {
	t.Parallel()
	testlib.SkipE2E(t)

	ctx := t.Context()
	pn := t.Name()
	dir := testlib.WriteTempFiles(t, map[string]string{
		"compose.yaml": `services:
  alpha:
    image: docker.io/alpine:edge
  beta:
    image: docker.io/alpine:edge
    depends_on:
      - alpha
`})
	compose := filepath.Join(dir, "compose.yaml")

	testlib.CleanupCompose(t, pn, "-f", compose, "down", "--project")

	_, err := testlib.RunCompose(ctx, t, pn, "", nil, "-f", compose, "up", "--detach", "--no-healthd")
	require.NoError(t, err)

	c := projectClient(ctx, t, pn)
	conn, err := c.Connection()
	require.NoError(t, err)

	uuid := func(name string) string {
		inst, _, err := conn.GetInstance(ctx, c.IncusProject(), name, nil)
		require.NoError(t, err)

		return inst.Config["volatile.uuid"]
	}

	alphaUUID := uuid("alpha-1")
	betaUUID := uuid("beta-1")
	require.NotEmpty(t, alphaUUID)
	require.NotEmpty(t, betaUUID)

	_, err = testlib.RunCompose(ctx, t, pn, "", nil, "-f", compose, "up", "--detach", "--recreate", "--no-healthd", "alpha")
	require.NoError(t, err)

	require.NotEqual(t, alphaUUID, uuid("alpha-1"), "alpha must be recreated")
	require.NotEqual(t, betaUUID, uuid("beta-1"), "beta must be recreated")
}

// TestE2EGlobalProjectFlag pins that --global-project moves what incus-compose
// shares across projects, the locks first, out of the default global project,
// so tenants of one server can each have their own.
func TestE2EGlobalProjectFlag(t *testing.T) {
	t.Parallel()
	testlib.SkipE2E(t)

	ctx := t.Context()
	pn := t.Name()
	compose := testlib.Fixture(t, "simple", "compose.yaml")
	global := testlib.ProjectName(pn) + "-global"

	gc, err := client.NewTestClient(context.Background())
	require.NoError(t, err)

	t.Cleanup(func() {
		if testlib.KeepTestData() {
			return
		}

		err := gc.DeleteProject(global, true)
		if err != nil {
			t.Errorf("deleting the global project %s: %v", global, err)
		}
	})

	args := []string{"--global-project", global, "-f", compose}
	testlib.CleanupCompose(t, pn, append(args, "down", "--project", "--no-dns", "--no-healthd")...)

	_, err = testlib.RunCompose(ctx, t, pn, "", nil, append(args, "up", "--detach", "--no-dns", "--no-healthd")...)
	require.NoError(t, err)

	c, err := gc.EnsureProject(global)
	require.NoError(t, err, "up did not create the global project it was given")

	locks, err := c.Resource(client.KindStorageVolume, locksVolume, &client.StorageVolumeConfig{})
	require.NoError(t, err)
	require.NoError(t, client.RunAction(ctx, locks, client.ActionEnsure), "the locks are not in the global project given")
}

// TestE2EUpRestrictedCertificate is a regression test for #127: `up` with a
// certificate restricted to the projects it uses. Incus hides the server
// config from such a certificate, which is where core.https_address lives.
func TestE2EUpRestrictedCertificate(t *testing.T) {
	t.Parallel()
	testlib.SkipE2E(t)

	ctx := t.Context()
	pn := t.Name()
	// A bridge lives in the default project, which a restricted certificate
	// cannot create networks in, so it attaches to one the admin made. The
	// service is named for this test: Incus refuses an instance DNS name a
	// parallel test already holds on the shared bridge. The healthcheck puts
	// healthd in use, so its skip is exercised along with the DNS one.
	body := `services:
  restricted:
    image: docker.io/alpine:edge
    healthcheck:
      test: ["CMD", "true"]
    networks:
      - br0

networks:
  br0:
    external: true
    x-incus-compose:
      network: incusbr0
`
	// healthd.external set in the file rather than by --external-healthd: up
	// must leave healthd to the admin either way, not mint it a trust token.
	dir := testlib.WriteTempFiles(t, map[string]string{
		"compose.yaml": "x-incus:\n  features.networks: \"false\"\n\n" + body,
		"external.yaml": "x-incus:\n  features.networks: \"false\"\n\n" +
			"x-incus-compose:\n  healthd:\n    external: true\n\n" + body,
	})
	compose := filepath.Join(dir, "compose.yaml")

	gc, err := client.NewTestClient(context.Background())
	require.NoError(t, err)

	if !gc.IsRemote() {
		t.Skip("a restricted certificate needs an https remote")
	}

	conn, err := gc.Connection()
	require.NoError(t, err)

	_, _, err = conn.GetNetwork(ctx, incusApi.ProjectDefaultName, "incusbr0")
	if err != nil {
		t.Skipf("No incusbr0: %v", err)
	}

	testlib.CleanupCompose(t, pn, "-f", compose, "down", "--project")

	// A restricted certificate cannot create or edit projects, so the admin
	// hands one over. Without the marks up sets, first, to see it say which.
	_, err = gc.EnsureProject(testlib.ProjectName(pn),
		client.EnsureProjectWithCreate(),
		client.EnsureProjectWithConfig(map[string]string{"features.networks": "false"}),
	)
	require.NoError(t, err)

	cache := os.Getenv("INCUS_COMPOSE_IMAGE_CACHE")
	if cache == "" {
		cache = client.DefaultCacheProject
	}

	// The cache and the global project, which holds the image locks, are
	// shared across projects, so the admin grants them rather than the
	// certificate creating them.
	for _, p := range []string{cache, defaultGlobalProject} {
		_, err = gc.EnsureProject(p, client.EnsureProjectWithCreate())
		require.NoError(t, err)
	}

	info, err := conn.GetConnectionInfo(ctx)
	require.NoError(t, err)

	name := "ic-test-" + client.SanitizeProjectName(pn)
	projects := []string{client.SanitizeProjectName(testlib.ProjectName(pn)), cache, defaultGlobalProject}

	t.Cleanup(func() {
		certs, err := conn.GetCertificates(context.Background())
		if err != nil {
			return
		}

		for _, cert := range certs {
			if cert.Name == name {
				_ = conn.DeleteCertificate(context.Background(), cert.Fingerprint)
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

	certDir := t.TempDir()
	_, err = incustrust.Enroll(ctx, incustrust.Config{
		Name:              name,
		URL:               info.URL,
		ServerFingerprint: info.CertificateFingerprint,
		DataDir:           certDir,
		Restricted:        true,
		Projects:          projects,
	}, token.String())
	require.NoError(t, err)

	restricted := []string{
		"--server-url", info.URL,
		"--client-cert-file", filepath.Join(certDir, incustrust.CertFile),
		"--client-key-file", filepath.Join(certDir, incustrust.KeyFile),
		"--server-fingerprint", info.CertificateFingerprint,
	}

	_, err = testlib.RunCompose(ctx, t, pn, "", nil, append(restricted, "-f", compose, "up", "--detach")...)
	require.ErrorContains(t, err, "certificate is restricted")
	require.ErrorContains(t, err, "incus project set")

	err = gc.UpdateProjectConfig(testlib.ProjectName(pn), map[string]string{
		managedKey:              "true",
		shared.HealthEnabledKey: "true",
	})
	require.NoError(t, err)

	// The sidecars enroll with a trust token, which only an unrestricted
	// certificate can mint: up passing at all is up skipping them.
	_, err = testlib.RunCompose(ctx, t, pn, "", nil, append(restricted, "-f", compose, "up", "--detach")...)
	require.NoError(t, err)

	external := filepath.Join(dir, "external.yaml")
	_, err = testlib.RunCompose(ctx, t, pn, "", nil, append(restricted, "-f", external, "up", "--detach")...)
	require.NoError(t, err)

	// Not --project: deleting the project is the admin's, like creating it.
	_, err = testlib.RunCompose(ctx, t, pn, "", nil, append(restricted, "-f", compose, "down")...)
	require.NoError(t, err)

	names, err := conn.GetInstanceNames(ctx, client.SanitizeProjectName(testlib.ProjectName(pn)), nil)
	require.NoError(t, err)
	require.Empty(t, names, "down left instances behind")
}
