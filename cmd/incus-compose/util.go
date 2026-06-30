package main

import (
	"slices"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
)

type filterResourcesArgs struct {
	OnlyServices     []string
	WithDependencies bool
	// Reverse includes services that depend on OnlyServices (reverse deps).
	// Use for stop/down; leave false for start/up which only need forward deps.
	Reverse      bool
	ExcludeKinds []client.Kind
}

func filterResources(p *project.Project, in map[string][]client.Resource, args filterResourcesArgs) map[string][]client.Resource {
	result := map[string][]client.Resource{}

	if len(args.OnlyServices) > 0 {
		for _, s := range args.OnlyServices {
			resources, ok := in[s]
			if !ok {
				continue
			}

			result[s] = resources
		}
	} else {
		result = in
	}

	if args.WithDependencies && len(args.OnlyServices) > 0 {
		if args.Reverse {
			// Reverse: pull in services that depend on OnlyServices (for stop/down).
			for _, svc := range p.Services {
				for depName := range svc.DependsOn {
					if !slices.Contains(args.OnlyServices, depName) {
						continue
					}

					resources, ok := in[svc.Name]
					if !ok {
						continue
					}

					result[svc.Name] = resources
				}
			}
		} else {
			// Forward: pull in services that OnlyServices depend on (for start/up).
			for _, s := range args.OnlyServices {
				svc, ok := p.Services[s]
				if !ok {
					continue
				}

				for depName := range svc.DependsOn {
					resources, ok := in[depName]
					if !ok {
						continue
					}

					result[depName] = resources
				}
			}
		}
	}

	if args.ExcludeKinds != nil {
		for n, res := range result {
			newRes := []client.Resource{}

			for _, r := range res {
				if r.Kind() == client.KindInstance || !slices.Contains(args.ExcludeKinds, r.Kind()) {
					newRes = append(newRes, r)
				}
			}

			result[n] = newRes
		}
	}

	return result
}
