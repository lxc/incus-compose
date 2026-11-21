package project

import (
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	incusApi "github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testClusterMembers() []incusApi.ClusterMember {
	return []incusApi.ClusterMember{
		{
			ServerName:   "node1",
			Status:       "Online",
			Architecture: "x86_64",
			ClusterMemberPut: incusApi.ClusterMemberPut{
				Roles:         []string{"database", "database-leader"},
				Groups:        []string{"default", "web"},
				FailureDomain: "az1",
				Config: map[string]string{
					"user.disk": "ssd",
					"user.tier": "frontend",
				},
			},
		},
		{
			ServerName:   "node2",
			Status:       "Online",
			Architecture: "x86_64",
			ClusterMemberPut: incusApi.ClusterMemberPut{
				Roles:         []string{"database"},
				Groups:        []string{"default", "web"},
				FailureDomain: "az1",
				Config: map[string]string{
					"user.disk": "hdd",
					"user.tier": "backend",
				},
			},
		},
		{
			ServerName:   "node3",
			Status:       "Online",
			Architecture: "aarch64",
			ClusterMemberPut: incusApi.ClusterMemberPut{
				Roles:         []string{"worker"},
				Groups:        []string{"default", "compute"},
				FailureDomain: "az2",
				Config: map[string]string{
					"user.disk": "ssd",
					"user.tier": "backend",
				},
			},
		},
		{
			ServerName:   "node4-offline",
			Status:       "Evacuated",
			Architecture: "x86_64",
			ClusterMemberPut: incusApi.ClusterMemberPut{
				Roles:         []string{"worker"},
				Groups:        []string{"default"},
				FailureDomain: "az2",
			},
		},
	}
}

func TestFilterClusterMembers(t *testing.T) {
	t.Parallel()

	members := testClusterMembers()

	t.Run("online only by default", func(t *testing.T) {
		t.Parallel()

		got, err := filterClusterMembers(members, nil)
		require.NoError(t, err)
		require.Len(t, got, 3)
	})

	t.Run("node.hostname and node.id", func(t *testing.T) {
		t.Parallel()

		got, err := filterClusterMembers(members, []string{"node.hostname == node2"})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "node2", got[0].ServerName)

		got, err = filterClusterMembers(members, []string{"node.id != node2"})
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, "node1", got[0].ServerName)
		assert.Equal(t, "node3", got[1].ServerName)
	})

	t.Run("node.role", func(t *testing.T) {
		t.Parallel()

		got, err := filterClusterMembers(members, []string{"node.role == database"})
		require.NoError(t, err)
		require.Len(t, got, 2)

		got, err = filterClusterMembers(members, []string{"node.role != database"})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "node3", got[0].ServerName)
	})

	t.Run("node.labels", func(t *testing.T) {
		t.Parallel()

		got, err := filterClusterMembers(members, []string{"node.labels.disk == ssd", "node.labels.tier == backend"})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "node3", got[0].ServerName)
	})

	t.Run("node.group and node.failure_domain", func(t *testing.T) {
		t.Parallel()

		got, err := filterClusterMembers(members, []string{"node.group == web"})
		require.NoError(t, err)
		require.Len(t, got, 2)

		got, err = filterClusterMembers(members, []string{"node.failure_domain == az2"})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "node3", got[0].ServerName)
	})

	t.Run("invalid constraint", func(t *testing.T) {
		t.Parallel()

		_, err := filterClusterMembers(members, []string{"invalid_syntax"})
		require.ErrorContains(t, err, "invalid placement constraint")

		_, err = filterClusterMembers(members, []string{"node.unknown == foo"})
		require.ErrorContains(t, err, "unsupported placement constraint key")
	})
}

func TestOrderClusterMembersSpread(t *testing.T) {
	t.Parallel()

	members := testClusterMembers()[:3] // node1 (az1, ssd), node2 (az1, hdd), node3 (az2, ssd)

	ordered := orderClusterMembers(members, []types.PlacementPreferences{
		{Spread: "node.failure_domain"},
		{Spread: "node.labels.disk"},
	}, "node2")
	require.Len(t, ordered, 3)
	// First bucket az1 (preferredServer node2 first), second bucket az2 (node3), then remaining az1 (node1)
	assert.Equal(t, []string{"node2", "node3", "node1"}, []string{
		ordered[0].ServerName,
		ordered[1].ServerName,
		ordered[2].ServerName,
	})
}

func TestResolveServicePlacementEnv(t *testing.T) {
	t.Parallel()

	members := testClusterMembers()

	t.Run("single replica defaults to connected server on bridge or local storage (#213)", func(t *testing.T) {
		t.Parallel()

		env := clusterPlacementEnv{
			clustered:   true,
			serverName:  "node2",
			networkType: "bridge",
			members:     members,
		}
		svc := types.ServiceConfig{Name: "web"}

		desired, targets, warn, err := resolveServicePlacementEnv(env, servicePlacementInput{service: svc}, false)
		require.NoError(t, err)
		assert.False(t, warn)
		assert.Equal(t, 1, desired)
		assert.Equal(t, []string{"node2"}, targets)

		// With OVN + remote storage and no explicit placement, target is left empty for Incus scheduler
		env.networkType = "ovn"
		desired, targets, warn, err = resolveServicePlacementEnv(env, servicePlacementInput{service: svc, remotePool: true}, false)
		require.NoError(t, err)
		assert.False(t, warn)
		assert.Equal(t, 1, desired)
		assert.Equal(t, []string{""}, targets)
	})

	t.Run("scaling on local storage fails by default and warns with override (#1)", func(t *testing.T) {
		t.Parallel()

		env := clusterPlacementEnv{
			clustered:   true,
			serverName:  "node1",
			networkType: "ovn",
			members:     members,
		}
		replicas := 2
		svc := types.ServiceConfig{
			Name: "web",
			Deploy: &types.DeployConfig{
				Replicas: &replicas,
			},
		}

		_, _, _, err := resolveServicePlacementEnv(env, servicePlacementInput{service: svc}, false)
		require.ErrorContains(t, err, "cannot deploy multiple replicas on a cluster without shared (remote) storage")

		desired, targets, warn, err := resolveServicePlacementEnv(env, servicePlacementInput{service: svc}, true)
		require.NoError(t, err)
		assert.True(t, warn)
		assert.Equal(t, 2, desired)
		assert.Equal(t, []string{"node1", "node2"}, targets)
	})

	t.Run("deploy.mode global and max_replicas_per_node on remote storage (#1)", func(t *testing.T) {
		t.Parallel()

		env := clusterPlacementEnv{
			clustered:   true,
			serverName:  "node1",
			networkType: "ovn",
			members:     members,
		}
		svc := types.ServiceConfig{
			Name: "agent",
			Deploy: &types.DeployConfig{
				Mode: "global",
				Placement: types.Placement{
					Constraints: []string{"node.role == database"},
				},
			},
		}

		desired, targets, warn, err := resolveServicePlacementEnv(env, servicePlacementInput{service: svc, remotePool: true}, false)
		require.NoError(t, err)
		assert.False(t, warn)
		assert.Equal(t, 2, desired)
		assert.Equal(t, []string{"node1", "node2"}, targets)

		// Exceeding max_replicas_per_node returns an error
		four := 4
		svc.Deploy.Mode = "replicated"
		svc.Deploy.Replicas = &four
		svc.Deploy.Placement.MaxReplicas = 1

		_, _, _, err = resolveServicePlacementEnv(env, servicePlacementInput{service: svc, remotePool: true}, false)
		require.ErrorContains(t, err, "exceed max_replicas_per_node")
	})

	t.Run("deploy.restart_policy fallback (#1)", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, "no", serviceRestartPolicy(types.ServiceConfig{
			Deploy: &types.DeployConfig{RestartPolicy: &types.RestartPolicy{Condition: "none"}},
		}))
		assert.Equal(t, "on-failure", serviceRestartPolicy(types.ServiceConfig{
			Deploy: &types.DeployConfig{RestartPolicy: &types.RestartPolicy{Condition: "on-failure"}},
		}))
		assert.Equal(t, "always", serviceRestartPolicy(types.ServiceConfig{
			Deploy: &types.DeployConfig{RestartPolicy: &types.RestartPolicy{Condition: "any"}},
		}))
		assert.Equal(t, "unless-stopped", serviceRestartPolicy(types.ServiceConfig{
			Restart: "unless-stopped",
			Deploy:  &types.DeployConfig{RestartPolicy: &types.RestartPolicy{Condition: "none"}},
		}))
	})
}

func TestResolveProjectPlacementEnv(t *testing.T) {
	t.Parallel()

	members := testClusterMembers()

	bridgeEnv := clusterPlacementEnv{
		clustered:   true,
		serverName:  "node1",
		networkType: "bridge",
		members:     members,
	}

	ovnEnv := clusterPlacementEnv{
		clustered:   true,
		serverName:  "node1",
		networkType: "ovn",
		members:     members,
	}

	pinned := func(hostname string) *types.DeployConfig {
		return &types.DeployConfig{Placement: types.Placement{Constraints: []string{"node.hostname == " + hostname}}}
	}

	replicas := func(n int) *types.DeployConfig {
		return &types.DeployConfig{Replicas: &n}
	}

	relations := func(affinity, anti []any) types.Extensions {
		ext := map[string]any{}
		if affinity != nil {
			ext["affinity"] = affinity
		}

		if anti != nil {
			ext["anti-affinity"] = anti
		}

		return types.Extensions{"x-incus-compose": ext}
	}

	onBridge := func(name string, deploy *types.DeployConfig, ext types.Extensions) servicePlacementInput {
		return servicePlacementInput{
			service: types.ServiceConfig{Name: name, Deploy: deploy, Extensions: ext},
			bridges: []string{"default"},
		}
	}

	onOVN := func(name string, deploy *types.DeployConfig, ext types.Extensions) servicePlacementInput {
		return servicePlacementInput{
			service:    types.ServiceConfig{Name: name, Deploy: deploy, Extensions: ext},
			remotePool: true,
		}
	}

	resolve := func(t *testing.T, env clusterPlacementEnv, allowLocal bool, inputs ...servicePlacementInput) (map[string][]string, []string, error) {
		t.Helper()

		byName := map[string]servicePlacementInput{}
		for _, in := range inputs {
			byName[in.service.Name] = in
		}

		var warnings []string
		placements, err := resolveProjectPlacementEnv(env, byName, allowLocal, func(msg string, _ ...any) {
			warnings = append(warnings, msg)
		})
		if err != nil {
			return nil, warnings, err
		}

		targets := map[string][]string{}
		for name, p := range placements {
			targets[name] = p.targets
		}

		return targets, warnings, nil
	}

	t.Run("a bridge peer follows a constrained service", func(t *testing.T) {
		t.Parallel()

		targets, _, err := resolve(t, bridgeEnv, false,
			onBridge("app", pinned("node3"), nil),
			onBridge("tunnel", nil, nil),
		)
		require.NoError(t, err)
		assert.Equal(t, map[string][]string{"app": {"node3"}, "tunnel": {"node3"}}, targets)
	})

	t.Run("a lone bridge service stays on the connected member", func(t *testing.T) {
		t.Parallel()

		in := onBridge("app", nil, nil)
		in.bridges = []string{"front"}

		targets, _, err := resolve(t, bridgeEnv, false, in, onBridge("tunnel", pinned("node3"), nil))
		require.NoError(t, err)
		assert.Equal(t, map[string][]string{"app": {"node1"}, "tunnel": {"node3"}}, targets)
	})

	t.Run("an existing instance pins its group over the connected member", func(t *testing.T) {
		t.Parallel()

		env := bridgeEnv
		env.locations = map[string]string{"tunnel-1": "node2"}

		targets, warnings, err := resolve(t, env, false,
			onBridge("app", nil, nil),
			onBridge("tunnel", nil, nil),
		)
		require.NoError(t, err)
		assert.Empty(t, warnings)
		assert.Equal(t, map[string][]string{"app": {"node2"}, "tunnel": {"node2"}}, targets)
	})

	t.Run("a group already split warns and keeps the first usable member", func(t *testing.T) {
		t.Parallel()

		env := bridgeEnv
		env.locations = map[string]string{"app-1": "node3", "tunnel-1": "node2"}

		targets, warnings, err := resolve(t, env, false,
			onBridge("app", nil, nil),
			onBridge("tunnel", nil, nil),
		)
		require.NoError(t, err)
		assert.Len(t, warnings, 1)
		assert.Equal(t, map[string][]string{"app": {"node3"}, "tunnel": {"node3"}}, targets)
	})

	t.Run("affinity co-locates on OVN with remote storage", func(t *testing.T) {
		t.Parallel()

		targets, _, err := resolve(t, ovnEnv, false,
			onOVN("app", nil, nil),
			onOVN("tunnel", nil, nil),
		)
		require.NoError(t, err)
		assert.Equal(t, map[string][]string{"app": {""}, "tunnel": {""}}, targets)

		targets, _, err = resolve(t, ovnEnv, false,
			onOVN("app", pinned("node2"), nil),
			onOVN("cache", nil, relations([]any{"app"}, nil)),
			onOVN("tunnel", nil, nil),
		)
		require.NoError(t, err)
		assert.Equal(t, map[string][]string{"app": {"node2"}, "cache": {"node2"}, "tunnel": {""}}, targets)
	})

	t.Run("affinity with a scaled service follows its members", func(t *testing.T) {
		t.Parallel()

		targets, _, err := resolve(t, ovnEnv, false,
			onOVN("web", replicas(2), nil),
			onOVN("sidecar", replicas(3), relations([]any{"web"}, nil)),
			onOVN("cache", nil, relations([]any{"web"}, nil)),
		)
		require.NoError(t, err)
		assert.Equal(t, []string{"node1", "node2"}, targets["web"])
		assert.Equal(t, []string{"node1", "node2", "node1"}, targets["sidecar"])
		assert.Equal(t, []string{"node1"}, targets["cache"])
	})

	t.Run("a scaled service follows a single one onto its member", func(t *testing.T) {
		t.Parallel()

		targets, _, err := resolve(t, ovnEnv, false,
			onOVN("db", pinned("node3"), nil),
			onOVN("web", replicas(2), relations([]any{"db"}, nil)),
		)
		require.NoError(t, err)
		assert.Equal(t, []string{"node3", "node3"}, targets["web"])
	})

	t.Run("a peer scaled to zero has nothing to follow", func(t *testing.T) {
		t.Parallel()

		targets, _, err := resolve(t, ovnEnv, false,
			onOVN("app", replicas(0), nil),
			onOVN("cache", nil, relations([]any{"app"}, nil)),
		)
		require.NoError(t, err)
		assert.Empty(t, targets["app"])
		assert.Equal(t, []string{""}, targets["cache"])
	})

	t.Run("anti-affinity avoids the members of a resolved peer", func(t *testing.T) {
		t.Parallel()

		targets, _, err := resolve(t, ovnEnv, false,
			onOVN("app", nil, nil),
			onOVN("db", nil, relations(nil, []any{"app"})),
		)
		require.NoError(t, err)
		assert.Equal(t, map[string][]string{"app": {"node1"}, "db": {"node2"}}, targets)

		targets, _, err = resolve(t, ovnEnv, false,
			onOVN("web", replicas(2), nil),
			onOVN("db", nil, relations(nil, []any{"web"})),
		)
		require.NoError(t, err)
		assert.Equal(t, []string{"node1"}, targets["db"])
		assert.Equal(t, []string{"node2", "node3"}, targets["web"])

		targets, _, err = resolve(t, ovnEnv, false,
			onOVN("a", pinned("node3"), nil),
			onOVN("web", replicas(2), relations(nil, []any{"a"})),
		)
		require.NoError(t, err)
		assert.Equal(t, []string{"node1", "node2"}, targets["web"])

		_, _, err = resolve(t, ovnEnv, false,
			onOVN("app", pinned("node2"), nil),
			onOVN("db", pinned("node2"), relations(nil, []any{"app"})),
		)
		require.ErrorContains(t, err, `service "db": no online cluster member satisfies`)
	})

	t.Run("bridge peers with conflicting constraints fail", func(t *testing.T) {
		t.Parallel()

		_, _, err := resolve(t, bridgeEnv, false,
			onBridge("app", pinned("node1"), nil),
			onBridge("tunnel", pinned("node2"), nil),
		)
		require.ErrorContains(t, err, `services "app", "tunnel" must share a cluster member`)
	})

	t.Run("anti-affinity within a group fails", func(t *testing.T) {
		t.Parallel()

		_, _, err := resolve(t, bridgeEnv, false,
			onBridge("app", nil, nil),
			onBridge("db", nil, relations(nil, []any{"app"})),
		)
		require.ErrorContains(t, err, "which must share its cluster member")

		_, _, err = resolve(t, ovnEnv, false,
			onOVN("app", nil, nil),
			onOVN("db", nil, relations([]any{"app"}, []any{"app"})),
		)
		require.ErrorContains(t, err, "which must share its cluster member")
	})

	t.Run("unknown and self references fail", func(t *testing.T) {
		t.Parallel()

		_, _, err := resolve(t, ovnEnv, false, onOVN("app", nil, relations([]any{"nope"}, nil)))
		require.ErrorContains(t, err, `names unknown service "nope"`)

		_, _, err = resolve(t, ovnEnv, false, onOVN("app", nil, relations(nil, []any{"app"})))
		require.ErrorContains(t, err, "cannot name the service itself")

		malformed := onOVN("cache", nil, types.Extensions{"x-incus-compose": map[string]any{"affinity": "app"}})
		_, _, err = resolve(t, ovnEnv, false, onOVN("app", nil, nil), malformed)
		require.ErrorContains(t, err, `service "cache": parsing x-incus-compose`)
	})

	t.Run("affinity between scaled services in a cycle fails", func(t *testing.T) {
		t.Parallel()

		_, _, err := resolve(t, ovnEnv, false,
			onOVN("a", replicas(2), relations([]any{"b"}, nil)),
			onOVN("b", replicas(2), relations([]any{"a"}, nil)),
		)
		require.ErrorContains(t, err, "forms a cycle")
	})

	t.Run("scaled and global services on a bridge are not grouped", func(t *testing.T) {
		t.Parallel()

		targets, _, err := resolve(t, bridgeEnv, true,
			onBridge("web", replicas(2), nil),
			onBridge("agent", &types.DeployConfig{Mode: "global"}, nil),
			onBridge("tunnel", pinned("node3"), nil),
		)
		require.NoError(t, err)
		assert.Equal(t, []string{"node1", "node2"}, targets["web"])
		assert.Equal(t, []string{"node1", "node2", "node3"}, targets["agent"])
		assert.Equal(t, []string{"node3"}, targets["tunnel"])
	})

	t.Run("placement does not depend on map order", func(t *testing.T) {
		t.Parallel()

		inputs := []servicePlacementInput{
			onBridge("e", nil, nil),
			onBridge("d", nil, relations(nil, nil)),
			onOVN("c", nil, relations(nil, []any{"e"})),
			onOVN("b", nil, relations([]any{"c"}, nil)),
			onOVN("a", replicas(2), relations(nil, []any{"b"})),
		}

		first, _, err := resolve(t, bridgeEnv, true, inputs...)
		require.NoError(t, err)

		for range 20 {
			again, _, err := resolve(t, bridgeEnv, true, inputs...)
			require.NoError(t, err)
			assert.Equal(t, first, again)
		}
	})

	t.Run("a standalone server ignores affinity and warns on anti-affinity", func(t *testing.T) {
		t.Parallel()

		targets, warnings, err := resolve(t, clusterPlacementEnv{}, false,
			onOVN("app", nil, nil),
			onOVN("cache", nil, relations([]any{"app"}, nil)),
			onOVN("db", nil, relations(nil, []any{"app"})),
		)
		require.NoError(t, err)
		assert.Equal(t, []string{"anti-affinity has no effect on a standalone server"}, warnings)
		assert.Equal(t, map[string][]string{"app": {""}, "cache": {""}, "db": {""}}, targets)
	})
}
