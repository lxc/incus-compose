package project

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	incusApi "github.com/lxc/incus/v7/shared/api"

	"github.com/lxc/incus-compose/client"
)

// serviceAllowLocalStorage reports whether service sets x-incus-compose.allow-local-storage: true.
func serviceAllowLocalStorage(service types.ServiceConfig) bool {
	var raw map[string]any
	ok, err := service.Extensions.Get("x-incus-compose", &raw)
	if !ok || err != nil {
		return false
	}

	allow, _ := raw["allow-local-storage"].(bool)

	return allow
}

// serviceRestartPolicy returns the effective restart policy for a service,
// falling back to deploy.restart_policy.condition when service.Restart is empty.
func serviceRestartPolicy(service types.ServiceConfig) string {
	if service.Restart != "" {
		return service.Restart
	}

	if service.Deploy == nil || service.Deploy.RestartPolicy == nil {
		return ""
	}

	switch service.Deploy.RestartPolicy.Condition {
	case "none":
		return "no"
	case "on-failure":
		return "on-failure"
	case "any", "":
		return "always"
	default:
		return service.Deploy.RestartPolicy.Condition
	}
}

// serviceAffinity returns the service names listed in
// x-incus-compose.affinity and x-incus-compose.anti-affinity.
func serviceAffinity(service types.ServiceConfig) ([]string, []string, error) {
	var ext struct {
		Affinity     []string `mapstructure:"affinity"`
		AntiAffinity []string `mapstructure:"anti-affinity"`
	}

	// Unlike allow-local-storage, a malformed value fails: ignoring
	// `affinity: app` would quietly place the services apart.
	_, err := service.Extensions.Get("x-incus-compose", &ext)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing x-incus-compose: %w", err)
	}

	return ext.Affinity, ext.AntiAffinity, nil
}

// serviceDeployMode returns deploy.mode, defaulting to replicated.
func serviceDeployMode(service types.ServiceConfig) string {
	if service.Deploy != nil && service.Deploy.Mode != "" {
		return service.Deploy.Mode
	}

	return "replicated"
}

// clusterPlacementEnv holds the cluster-wide state placement is resolved against.
type clusterPlacementEnv struct {
	clustered   bool
	serverName  string
	networkType string
	members     []incusApi.ClusterMember

	// locations maps the Incus name of every existing instance to the member
	// it runs on.
	locations map[string]string
}

// servicePlacementInput holds what placement reads about one service.
type servicePlacementInput struct {
	service       types.ServiceConfig
	scaleOverride *int
	remotePool    bool

	// bridges names the service's networks that every cluster member holds a
	// separate copy of, so peers on them only reach each other on one member.
	bridges []string
}

// servicePlacement is the replica count and per-replica cluster targets
// resolved for a service.
type servicePlacement struct {
	desired int
	targets []string
}

// resolveProjectPlacement resolves the placement of every service, keyed by
// service name.
func (p *Project) resolveProjectPlacement(c *client.Client, scale map[string]int) (map[string]servicePlacement, error) {
	clustered := c != nil && c.Global().IsClustered()

	var env clusterPlacementEnv
	if clustered {
		members, err := c.Global().ClusterMembers()
		if err != nil {
			return nil, fmt.Errorf("listing cluster members: %w", err)
		}

		locations, err := c.InstanceLocations()
		if err != nil {
			return nil, err
		}

		env = clusterPlacementEnv{
			clustered:   true,
			serverName:  c.Global().ServerName(),
			networkType: c.NetworkType(),
			members:     members,
			locations:   locations,
		}
	}

	inputs := make(map[string]servicePlacementInput, len(p.Services))
	for name, service := range p.Services {
		in := servicePlacementInput{service: service}

		s, ok := scale[name]
		if ok {
			in.scaleOverride = &s
		}

		if clustered {
			remotePool, err := p.isServiceStorageRemote(c, service)
			if err != nil {
				return nil, fmt.Errorf("service %q: checking storage pool: %w", name, err)
			}

			in.remotePool = remotePool

			// The type of an external network is only known to Incus; one
			// without a driver is taken to be of the project's default type.
			for netName := range service.Networks {
				driver := p.Networks[netName].Driver
				if driver == "bridge" || ((driver == "" || driver == "auto") && !c.FeaturesNetworks()) {
					in.bridges = append(in.bridges, netName)
				}
			}
		}

		inputs[name] = in
	}

	warn := func(msg string, args ...any) {
		if c != nil {
			c.LogWarn(msg, args...)
		}
	}

	return resolveProjectPlacementEnv(env, inputs, p.ClientConfig.AllowLocalStorage, warn)
}

func resolveProjectPlacementEnv(env clusterPlacementEnv, inputs map[string]servicePlacementInput, projectAllowLocal bool, warn func(msg string, args ...any)) (map[string]servicePlacement, error) {
	names := slices.Sorted(maps.Keys(inputs))

	var errs error
	affinity := map[string][]string{}
	anti := map[string][]string{}
	for _, name := range names {
		aff, apart, err := serviceAffinity(inputs[name].service)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("service %q: %w", name, err))
			continue
		}

		for _, peer := range slices.Concat(aff, apart) {
			_, ok := inputs[peer]
			switch {
			case peer == name:
				errs = errors.Join(errs, fmt.Errorf("service %q: affinity and anti-affinity cannot name the service itself", name))
			case !ok:
				errs = errors.Join(errs, fmt.Errorf("service %q: affinity or anti-affinity names unknown service %q", name, peer))
			}
		}

		if !env.clustered && len(apart) > 0 {
			warn("anti-affinity has no effect on a standalone server", "service", name)
		}

		affinity[name] = aff
		for _, peer := range apart {
			if !slices.Contains(anti[name], peer) {
				anti[name] = append(anti[name], peer)
				anti[peer] = append(anti[peer], name)
			}
		}
	}

	placements := map[string]servicePlacement{}
	for _, name := range names {
		desired, targets, warnLocal, err := resolveServicePlacementEnv(env, inputs[name], projectAllowLocal)
		if err != nil {
			errs = errors.Join(errs, err)
			continue
		}

		if warnLocal {
			warn(
				"Deploying multiple replicas on a cluster with local storage; volumes and instances are member-local",
				"service", name,
				"replicas", desired,
			)
		}

		placements[name] = servicePlacement{desired: desired, targets: targets}
	}

	if errs != nil {
		return nil, errs
	}

	if !env.clustered {
		return placements, nil
	}

	err := colocateServices(env, inputs, placements, affinity, anti, projectAllowLocal, warn)
	if err != nil {
		return nil, err
	}

	return placements, nil
}

// colocateServices rewrites placements so that services sharing a bridge
// network or naming each other in affinity share a cluster member, and
// services naming each other in anti-affinity do not.
//
// Single-replica services that must share a member form one group and get one
// member. A scaled or global service is never grouped implicitly: it follows
// the members of the services its affinity names, and is otherwise resolved
// as before on the members anti-affinity leaves it. Groups and services are
// resolved in dependency order, ties broken by name, and each one avoids the
// members of the anti-affinity peers resolved before it.
func colocateServices(
	env clusterPlacementEnv,
	inputs map[string]servicePlacementInput,
	placements map[string]servicePlacement,
	affinity map[string][]string,
	anti map[string][]string,
	projectAllowLocal bool,
	warn func(msg string, args ...any),
) error {
	names := slices.Sorted(maps.Keys(placements))

	single := func(name string) bool {
		return placements[name].desired == 1 && serviceDeployMode(inputs[name].service) != "global"
	}

	root := map[string]string{}
	for _, name := range names {
		root[name] = name
	}

	find := func(name string) string {
		for root[name] != name {
			name = root[name]
		}

		return name
	}

	// The smallest name roots a group, so the order groups resolve in does
	// not depend on which edge joined them.
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if rb < ra {
			ra, rb = rb, ra
		}

		root[rb] = ra
	}

	// Services left out of every relation keep their placement.
	related := map[string]bool{}

	onBridge := map[string]string{}
	for _, name := range names {
		if !single(name) {
			continue
		}

		for _, bridge := range inputs[name].bridges {
			first, ok := onBridge[bridge]
			if !ok {
				onBridge[bridge] = name
				continue
			}

			union(first, name)
			related[first], related[name] = true, true
		}
	}

	var errs error
	follows := map[string][]string{}
	for _, name := range names {
		if len(anti[name]) > 0 {
			related[name] = true
		}

		for _, peer := range affinity[name] {
			// A peer scaled to zero has no member to follow.
			if placements[peer].desired == 0 {
				continue
			}

			related[name], related[peer] = true, true

			if single(name) && single(peer) {
				union(name, peer)
				continue
			}

			follows[name] = append(follows[name], peer)
		}
	}

	for _, name := range names {
		for _, peer := range anti[name] {
			if name < peer && single(name) && single(peer) && find(name) == find(peer) {
				errs = errors.Join(errs, fmt.Errorf(
					"service %q: anti-affinity with service %q, which must share its cluster member (shared bridge network or affinity)",
					name, peer,
				))
			}
		}
	}

	if errs != nil {
		return errs
	}

	nodes := map[string][]string{}
	for _, name := range names {
		if placements[name].desired == 0 || !related[name] {
			continue
		}

		node := name
		if single(name) {
			node = find(name)
		}

		nodes[node] = append(nodes[node], name)
	}

	deps := map[string][]string{}
	for node, members := range nodes {
		for _, name := range members {
			for _, peer := range follows[name] {
				peerNode := peer
				if single(peer) {
					peerNode = find(peer)
				}

				if peerNode != node {
					deps[node] = append(deps[node], peerNode)
				}
			}
		}
	}

	done := map[string]bool{}
	resolved := map[string]bool{}
	for len(done) < len(nodes) {
		var node string
		for _, candidate := range slices.Sorted(maps.Keys(nodes)) {
			if done[candidate] {
				continue
			}

			ready := true
			for _, dep := range deps[candidate] {
				if !done[dep] {
					ready = false
					break
				}
			}

			if ready {
				node = candidate
				break
			}
		}

		if node == "" {
			var pending []string
			for _, candidate := range slices.Sorted(maps.Keys(nodes)) {
				if !done[candidate] {
					pending = append(pending, nodes[candidate]...)
				}
			}

			return fmt.Errorf("services %s: affinity between scaled services forms a cycle", quoteJoin(pending))
		}

		done[node] = true
		members := nodes[node]

		// Anti-affinity is symmetric: only peers resolved earlier hold a
		// member to avoid, and the later of the two avoids the earlier.
		var avoid []string
		for _, name := range members {
			for _, peer := range anti[name] {
				if resolved[peer] {
					avoid = append(avoid, placements[peer].targets...)
				}
			}
		}

		var err error
		if single(members[0]) {
			err = placeGroup(env, inputs, placements, follows, members, avoid, warn)
		} else {
			err = placeScaled(env, inputs[node], placements, follows[node], avoid, projectAllowLocal)
		}

		if err != nil {
			return err
		}

		for _, name := range members {
			resolved[name] = true
		}
	}

	return nil
}

// placeGroup puts every single-replica service of a group on one member.
func placeGroup(
	env clusterPlacementEnv,
	inputs map[string]servicePlacementInput,
	placements map[string]servicePlacement,
	follows map[string][]string,
	group []string,
	avoid []string,
	warn func(msg string, args ...any),
) error {
	candidates := env.members
	for _, name := range group {
		var constraints []string
		service := inputs[name].service
		if service.Deploy != nil {
			constraints = service.Deploy.Placement.Constraints
		}

		allowed, err := filterClusterMembers(candidates, constraints)
		if err != nil {
			return fmt.Errorf("service %q: %w", name, err)
		}

		for _, peer := range follows[name] {
			allowed = slices.DeleteFunc(allowed, func(m incusApi.ClusterMember) bool {
				return !slices.Contains(placements[peer].targets, m.ServerName)
			})
		}

		candidates = allowed
	}

	candidates = slices.DeleteFunc(slices.Clone(candidates), func(m incusApi.ClusterMember) bool {
		return slices.Contains(avoid, m.ServerName)
	})

	if len(candidates) == 0 {
		if len(group) == 1 {
			return fmt.Errorf("service %q: no online cluster member satisfies placement constraints, affinity and anti-affinity", group[0])
		}

		return fmt.Errorf("services %s must share a cluster member, but none online satisfies their placement constraints, affinity and anti-affinity", quoteJoin(group))
	}

	// An existing instance pins the group, so recreating one of its services
	// through another member does not split it.
	var running []string
	for _, name := range group {
		location := env.locations[client.SanitizeIncusName(instanceName(inputs[name].service, 1, 1), -1)]
		if location != "" && !slices.Contains(running, location) {
			running = append(running, location)
		}
	}

	if len(running) > 1 {
		warn("Services that must share a cluster member run on different members", "services", group, "members", running)
	}

	member := orderClusterMembers(candidates, nil, env.serverName)[0].ServerName
	for _, location := range running {
		if slices.ContainsFunc(candidates, func(m incusApi.ClusterMember) bool { return m.ServerName == location }) {
			member = location
			break
		}
	}

	for _, name := range group {
		placements[name] = servicePlacement{desired: 1, targets: []string{member}}
	}

	return nil
}

// placeScaled resolves a scaled or global service: onto the members of the
// first service its affinity names, or as resolveServicePlacementEnv would on
// the members anti-affinity leaves it.
func placeScaled(
	env clusterPlacementEnv,
	in servicePlacementInput,
	placements map[string]servicePlacement,
	follows []string,
	avoid []string,
	projectAllowLocal bool,
) error {
	name := in.service.Name

	if len(follows) == 0 {
		if len(avoid) == 0 {
			return nil
		}

		env.members = slices.DeleteFunc(slices.Clone(env.members), func(m incusApi.ClusterMember) bool {
			return slices.Contains(avoid, m.ServerName)
		})

		desired, targets, _, err := resolveServicePlacementEnv(env, in, projectAllowLocal)
		if err != nil {
			return fmt.Errorf("%w (after anti-affinity)", err)
		}

		placements[name] = servicePlacement{desired: desired, targets: targets}

		return nil
	}

	var constraints []string
	var maxReplicas uint64
	if in.service.Deploy != nil {
		constraints = in.service.Deploy.Placement.Constraints
		maxReplicas = in.service.Deploy.Placement.MaxReplicas
	}

	allowed, err := filterClusterMembers(env.members, constraints)
	if err != nil {
		return fmt.Errorf("service %q: %w", name, err)
	}

	lead := placements[follows[0]].targets
	desired := placements[name].desired
	targets := make([]string, desired)
	perMember := map[string]uint64{}
	for i := range desired {
		target := lead[i%len(lead)]

		ok := slices.ContainsFunc(allowed, func(m incusApi.ClusterMember) bool { return m.ServerName == target }) &&
			!slices.Contains(avoid, target)
		for _, peer := range follows[1:] {
			ok = ok && slices.Contains(placements[peer].targets, target)
		}

		if !ok {
			return fmt.Errorf("service %q: cluster member %q, where affinity places replica %d, does not satisfy placement constraints, affinity and anti-affinity", name, target, i+1)
		}

		perMember[target]++
		if maxReplicas > 0 && perMember[target] > maxReplicas {
			return fmt.Errorf("service %q: affinity places more than max_replicas_per_node (%d) replicas on cluster member %q", name, maxReplicas, target)
		}

		targets[i] = target
	}

	placements[name] = servicePlacement{desired: desired, targets: targets}

	return nil
}

func quoteJoin(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = strconv.Quote(name)
	}

	return strings.Join(quoted, ", ")
}

func resolveServicePlacementEnv(env clusterPlacementEnv, in servicePlacementInput, projectAllowLocal bool) (int, []string, bool, error) {
	mode := serviceDeployMode(in.service)
	switch mode {
	case "replicated", "global":
	default:
		return 0, nil, false, fmt.Errorf("service %q: unsupported deploy.mode %q (must be replicated or global)", in.service.Name, mode)
	}

	desired := 1
	if in.scaleOverride != nil {
		desired = *in.scaleOverride
	} else if in.service.Deploy != nil && in.service.Deploy.Replicas != nil {
		desired = *in.service.Deploy.Replicas
	}

	if !env.clustered {
		if mode == "global" && in.scaleOverride == nil {
			desired = 1
		}

		return desired, make([]string, desired), false, nil
	}

	var constraints []string
	var preferences []types.PlacementPreferences
	var maxReplicas uint64
	if in.service.Deploy != nil {
		constraints = in.service.Deploy.Placement.Constraints
		preferences = in.service.Deploy.Placement.Preferences
		maxReplicas = in.service.Deploy.Placement.MaxReplicas
	}

	candidates, err := filterClusterMembers(env.members, constraints)
	if err != nil {
		return 0, nil, false, fmt.Errorf("service %q: %w", in.service.Name, err)
	}

	if len(candidates) == 0 {
		return 0, nil, false, fmt.Errorf("service %q: no online cluster members satisfy placement constraints", in.service.Name)
	}

	if mode == "global" && in.scaleOverride == nil {
		desired = len(candidates)
	}

	if desired == 0 {
		return 0, nil, false, nil
	}

	if maxReplicas > 0 && uint64(desired) > uint64(len(candidates))*maxReplicas {
		return 0, nil, false, fmt.Errorf(
			"service %q: desired replicas (%d) exceed max_replicas_per_node (%d) across %d matching cluster member(s)",
			in.service.Name, desired, maxReplicas, len(candidates),
		)
	}

	scales := desired > 1 || mode == "global"
	allowLocal := projectAllowLocal || serviceAllowLocalStorage(in.service)
	warnLocal := false

	if scales && !in.remotePool {
		if !allowLocal {
			return 0, nil, false, fmt.Errorf(
				"service %q: cannot deploy multiple replicas on a cluster without shared (remote) storage; use a remote storage pool or set x-incus-compose.allow-local-storage: true (--allow-local-storage)",
				in.service.Name,
			)
		}

		warnLocal = true
	}

	hasPlacement := mode == "global" || len(constraints) > 0 || len(preferences) > 0 || maxReplicas > 0

	// Single replica with no explicit placement co-locates on the connected
	// cluster member when using a bridge network or a local storage pool (#213).
	if desired == 1 && !hasPlacement {
		if env.networkType == "bridge" || !in.remotePool {
			return 1, []string{env.serverName}, warnLocal, nil
		}

		return 1, []string{""}, warnLocal, nil
	}

	if mode == "global" && in.scaleOverride == nil {
		targets := make([]string, len(candidates))
		for i, m := range candidates {
			targets[i] = m.ServerName
		}

		return desired, targets, warnLocal, nil
	}

	ordered := orderClusterMembers(candidates, preferences, env.serverName)
	targets := make([]string, desired)
	for i := range desired {
		targets[i] = ordered[i%len(ordered)].ServerName
	}

	return desired, targets, warnLocal, nil
}

// isServiceStorageRemote reports whether all storage pools used by the service
// are remote (shared) cluster storage pools.
func (p *Project) isServiceStorageRemote(c *client.Client, service types.ServiceConfig) (bool, error) {
	defaultRemote, err := c.Global().IsStoragePoolRemote(c.Config().DefaultStoragePool)
	if err != nil {
		return false, err
	}
	if !defaultRemote {
		return false, nil
	}

	for _, cVol := range service.Volumes {
		volType := cVol.Type
		if volType == "" && cVol.Source != "" && !strings.HasPrefix(cVol.Source, "/") && !strings.HasPrefix(cVol.Source, ".") {
			volType = "volume"
		}
		if volType != "volume" {
			continue
		}

		volDef := p.Volumes[cVol.Source]
		pool := volumeXIncusComposePool(volDef)
		if pool == "" {
			continue
		}

		remote, err := c.Global().IsStoragePoolRemote(pool)
		if err != nil {
			return false, err
		}
		if !remote {
			return false, nil
		}
	}

	return true, nil
}

// filterClusterMembers filters online cluster members against Compose placement constraints.
func filterClusterMembers(members []incusApi.ClusterMember, constraints []string) ([]incusApi.ClusterMember, error) {
	parsed := make([]placementConstraint, 0, len(constraints))
	for _, raw := range constraints {
		pc, err := parsePlacementConstraint(raw)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, pc)
	}

	var out []incusApi.ClusterMember
	for _, m := range members {
		if m.Status != "" && !strings.EqualFold(m.Status, "Online") {
			continue
		}

		matched := true
		for _, pc := range parsed {
			if !pc.matches(m) {
				matched = false
				break
			}
		}

		if matched {
			out = append(out, m)
		}
	}

	return out, nil
}

type placementConstraint struct {
	key   string
	op    string
	value string
}

func parsePlacementConstraint(raw string) (placementConstraint, error) {
	for _, op := range []string{"==", "!="} {
		lhs, rhs, ok := strings.Cut(raw, op)
		if !ok {
			continue
		}

		lhs = strings.TrimSpace(lhs)
		rhs = strings.TrimSpace(rhs)
		if lhs == "" {
			break
		}

		switch {
		case lhs == "node.hostname",
			lhs == "node.id",
			lhs == "node.role",
			lhs == "node.group",
			lhs == "node.failure_domain",
			strings.HasPrefix(lhs, "node.labels."):
			return placementConstraint{key: lhs, op: op, value: rhs}, nil
		default:
			return placementConstraint{}, fmt.Errorf("unsupported placement constraint key %q in %q", lhs, raw)
		}
	}

	return placementConstraint{}, fmt.Errorf("invalid placement constraint %q (expected '<key> == <value>' or '<key> != <value>')", raw)
}

func (c placementConstraint) matches(m incusApi.ClusterMember) bool {
	var match bool

	switch {
	case c.key == "node.hostname" || c.key == "node.id":
		match = m.ServerName == c.value
	case c.key == "node.role":
		match = slices.Contains(m.Roles, c.value)
	case c.key == "node.group":
		match = slices.Contains(m.Groups, c.value)
	case c.key == "node.failure_domain":
		match = m.FailureDomain == c.value
	case strings.HasPrefix(c.key, "node.labels."):
		labelKey := strings.TrimPrefix(c.key, "node.labels.")
		match = m.Config["user."+labelKey] == c.value
	}

	if c.op == "!=" {
		return !match
	}

	return match
}

// orderClusterMembers orders candidate cluster members to spread replicas evenly
// across the buckets named by preferences (or by server name, starting with
// the connected server when no spread preference is given).
func orderClusterMembers(candidates []incusApi.ClusterMember, preferences []types.PlacementPreferences, preferredServer string) []incusApi.ClusterMember {
	sorted := slices.Clone(candidates)
	slices.SortFunc(sorted, func(a, b incusApi.ClusterMember) int {
		if a.ServerName == preferredServer && b.ServerName != preferredServer {
			return -1
		}
		if b.ServerName == preferredServer && a.ServerName != preferredServer {
			return 1
		}
		return strings.Compare(a.ServerName, b.ServerName)
	})

	if len(preferences) == 0 || len(sorted) <= 1 {
		return sorted
	}

	spreadKey := strings.TrimSpace(preferences[0].Spread)
	rest := preferences[1:]
	if spreadKey == "" {
		return orderClusterMembers(sorted, rest, preferredServer)
	}

	var bucketOrder []string
	buckets := map[string][]incusApi.ClusterMember{}
	for _, m := range sorted {
		val := memberSpreadValue(m, spreadKey)
		_, exists := buckets[val]
		if !exists {
			bucketOrder = append(bucketOrder, val)
		}
		buckets[val] = append(buckets[val], m)
	}

	if len(rest) > 0 {
		for _, bKey := range bucketOrder {
			buckets[bKey] = orderClusterMembers(buckets[bKey], rest, preferredServer)
		}
	}

	var out []incusApi.ClusterMember
	for len(out) < len(sorted) {
		for _, bKey := range bucketOrder {
			if len(buckets[bKey]) > 0 {
				out = append(out, buckets[bKey][0])
				buckets[bKey] = buckets[bKey][1:]
			}
		}
	}

	return out
}

func memberSpreadValue(m incusApi.ClusterMember, spreadKey string) string {
	switch {
	case spreadKey == "node.failure_domain":
		return m.FailureDomain
	case spreadKey == "node.hostname" || spreadKey == "node.id":
		return m.ServerName
	case spreadKey == "node.group":
		if len(m.Groups) > 0 {
			return m.Groups[0]
		}
		return ""
	case strings.HasPrefix(spreadKey, "node.labels."):
		labelKey := strings.TrimPrefix(spreadKey, "node.labels.")
		return m.Config["user."+labelKey]
	default:
		return m.ServerName
	}
}
