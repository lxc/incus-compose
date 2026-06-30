package main

import (
	"sort"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/stretchr/testify/assert"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
)

// mockResource satisfies client.Resource for testing without a real Incus connection.
type mockResource struct {
	name     string
	kind     client.Kind
	priority int
	ensured  bool
	created  bool
}

func newMockResource(name string, kind client.Kind) *mockResource {
	return &mockResource{name: name, kind: kind, priority: client.PriorityInstance}
}

func (m *mockResource) Name() string      { return m.name }
func (m *mockResource) IncusName() string { return m.name }
func (m *mockResource) Kind() client.Kind { return m.kind }
func (m *mockResource) Priority() int     { return m.priority }
func (m *mockResource) IsEnsured() bool   { return m.ensured }
func (m *mockResource) Created() bool     { return m.created }

var _ client.Resource = (*mockResource)(nil)

// newProject builds a minimal project.Project from a types.Project for testing.
func newProject(tp *types.Project) *project.Project {
	p := project.New()
	p.Project = tp
	return p
}

// resourceNames returns sorted resource names for deterministic assertions.
func resourceNames(res []client.Resource) []string {
	names := make([]string, len(res))
	for i, r := range res {
		names[i] = r.Name()
	}
	sort.Strings(names)
	return names
}

// resultKeys returns sorted keys of the result map.
func resultKeys(m map[string][]client.Resource) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestFilterResources(t *testing.T) {
	t.Parallel()

	webInst := newMockResource("web", client.KindInstance)
	webNet := newMockResource("net-web", client.KindNetwork)
	dbInst := newMockResource("db", client.KindInstance)
	dbVol := newMockResource("vol-db", client.KindStorageVolume)

	input := map[string][]client.Resource{
		"web": {webInst, webNet},
		"db":  {dbInst, dbVol},
	}

	webProject := newProject(&types.Project{
		Services: types.Services{
			"web": types.ServiceConfig{
				Name: "web",
				DependsOn: types.DependsOnConfig{
					"db": types.ServiceDependency{Condition: types.ServiceConditionStarted},
				},
			},
			"db": types.ServiceConfig{Name: "db"},
		},
	})

	emptyProject := newProject(&types.Project{Services: types.Services{}})

	tests := []struct {
		name     string
		p        *project.Project
		in       map[string][]client.Resource
		args     filterResourcesArgs
		wantKeys []string
		check    func(t *testing.T, got map[string][]client.Resource)
	}{
		{
			name:     "no_filter_returns_all",
			p:        emptyProject,
			in:       input,
			args:     filterResourcesArgs{},
			wantKeys: []string{"db", "web"},
		},
		{
			name:     "only_services_single",
			p:        emptyProject,
			in:       input,
			args:     filterResourcesArgs{OnlyServices: []string{"web"}},
			wantKeys: []string{"web"},
		},
		{
			name:     "only_services_both",
			p:        emptyProject,
			in:       input,
			args:     filterResourcesArgs{OnlyServices: []string{"web", "db"}},
			wantKeys: []string{"db", "web"},
		},
		{
			name:     "only_services_unknown_skipped",
			p:        emptyProject,
			in:       input,
			args:     filterResourcesArgs{OnlyServices: []string{"missing"}},
			wantKeys: []string{},
		},
		{
			name:     "with_dependencies_adds_dep_key",
			p:        webProject,
			in:       input,
			args:     filterResourcesArgs{OnlyServices: []string{"web"}, WithDependencies: true},
			wantKeys: []string{"db", "web"},
			check: func(t *testing.T, got map[string][]client.Resource) {
				// dep resources land under their own key, not under "web"
				assert.Equal(t, []string{"db", "vol-db"}, resourceNames(got["db"]))
				assert.Equal(t, []string{"net-web", "web"}, resourceNames(got["web"]))
			},
		},
		{
			name:     "with_dependencies_no_only_services_ignored",
			p:        webProject,
			in:       input,
			args:     filterResourcesArgs{WithDependencies: true},
			wantKeys: []string{"db", "web"},
		},
		{
			name:     "with_dependencies_dep_absent_from_resources",
			p:        webProject,
			in:       map[string][]client.Resource{"web": {webInst}}, // "db" not in map
			args:     filterResourcesArgs{OnlyServices: []string{"web"}, WithDependencies: true},
			wantKeys: []string{"web"}, // dep skipped gracefully
		},
		{
			name: "exclude_kinds_removes_non_instance",
			p:    emptyProject,
			in:   input,
			args: filterResourcesArgs{ExcludeKinds: []client.Kind{client.KindNetwork, client.KindStorageVolume}},
			check: func(t *testing.T, got map[string][]client.Resource) {
				assert.Equal(t, []string{"web"}, resourceNames(got["web"]))
				assert.Equal(t, []string{"db"}, resourceNames(got["db"]))
			},
		},
		{
			name: "exclude_kinds_never_removes_instance",
			p:    emptyProject,
			in:   input,
			args: filterResourcesArgs{ExcludeKinds: []client.Kind{client.KindInstance}},
			check: func(t *testing.T, got map[string][]client.Resource) {
				// KindInstance in ExcludeKinds is a no-op; instances always kept
				assert.Contains(t, resourceNames(got["web"]), "web")
				assert.Contains(t, resourceNames(got["db"]), "db")
			},
		},
		{
			name:     "exclude_kinds_nil_no_filtering",
			p:        emptyProject,
			in:       input,
			args:     filterResourcesArgs{ExcludeKinds: nil},
			wantKeys: []string{"db", "web"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := filterResources(tt.p, tt.in, tt.args)
			if tt.wantKeys != nil {
				assert.Equal(t, tt.wantKeys, resultKeys(got))
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func TestFlattenResources(t *testing.T) {
	t.Parallel()

	a := newMockResource("a", client.KindInstance)
	b := newMockResource("b", client.KindNetwork)
	c := newMockResource("c", client.KindStorageVolume)

	tests := []struct {
		name      string
		in        map[string][]client.Resource
		wantNames []string
	}{
		{
			name:      "empty",
			in:        map[string][]client.Resource{},
			wantNames: []string{},
		},
		{
			name:      "single_service",
			in:        map[string][]client.Resource{"web": {a, b}},
			wantNames: []string{"a", "b"},
		},
		{
			name:      "multiple_services",
			in:        map[string][]client.Resource{"web": {a, b}, "db": {c}},
			wantNames: []string{"a", "b", "c"},
		},
		{
			name:      "nil_slice_value",
			in:        map[string][]client.Resource{"web": nil},
			wantNames: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := flattenResources(tt.in)
			assert.Equal(t, tt.wantNames, resourceNames(got))
		})
	}
}
