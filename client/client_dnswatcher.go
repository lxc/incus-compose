package client

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
)

// dnsIPWaitTimeout bounds how long to wait for a freshly started instance to
// acquire its DHCP lease before recording its DNS address.
const dnsIPWaitTimeout = 15 * time.Second

// DNSmasqParse parses raw.dnsmasq address lines into a service->[]IP map.
func DNSmasqParse(raw string) map[string][]string {
	result := map[string][]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "address=/") {
			continue
		}
		rest := line[len("address=/"):]
		slash := strings.Index(rest, "/")
		if slash < 1 {
			continue
		}
		svc, ip := rest[:slash], rest[slash+1:]
		if ip != "" {
			result[svc] = append(result[svc], ip)
		}
	}
	return result
}

// dnsmasqRecords builds the raw.dnsmasq content: one "address" record per
// service IP, sorted by service name for deterministic output.
func dnsmasqRecords(serviceIPs map[string][]string) string {
	var b strings.Builder
	for _, service := range slices.Sorted(maps.Keys(serviceIPs)) {
		for _, ip := range serviceIPs[service] {
			fmt.Fprintf(&b, "address=/%s/%s\n", service, ip)
		}
	}
	return b.String()
}

// RegisterDNSWatcher wires service-name DNS records into the project's managed
// networks via the client lifecycle hooks. On each instance create/start/stop/
// delete it reads raw.dnsmasq from Incus, updates only the records for services
// seen in this run (identified via IncusName), preserves all other records, and
// writes back. Multiple projects can coexist in the same network without
// clobbering each other's records.
func (c *Client) RegisterDNSWatcher() error {
	networks := map[string]*Network{}
	instances := map[string]*Instance{}
	instanceIPs := map[string][]InterfaceIPs{}
	ownedSet := map[string]struct{}{} // dnsmasq keys this run owns
	mu := &sync.Mutex{}

	c.AddHookAfter(func(ctx context.Context, action Action, r Resource, _ Options, err error) error {
		if err != nil || !r.IsEnsured() {
			return err
		}

		mu.Lock()
		defer mu.Unlock()

		switch r.Kind() {
		case KindNetwork:
			net, ok := r.(*Network)
			if ok && action == ActionEnsure && !net.Config.External {
				networks[net.IncusName()] = net
				c.LogDebug("DNSWatcher network", "network", net.Name())
			}

		case KindInstance:
			inst, ok := r.(*Instance)
			if !ok {
				return err
			}

			svcKey := inst.ServiceName()
			ownedSet[svcKey] = struct{}{}

			changed := false
			switch action {
			case ActionEnsure:
				if !inst.Created() && inst.Running() {
					ips, ipErr := inst.WaitIPs(ctx, dnsIPWaitTimeout)
					if ipErr != nil {
						return ipErr
					}

					instances[inst.IncusName()] = inst
					instanceIPs[inst.IncusName()] = ips

					changed = true
				}
			case ActionStart:
				ips, ipErr := inst.WaitIPs(ctx, dnsIPWaitTimeout)
				if ipErr != nil {
					return ipErr
				}

				instances[inst.IncusName()] = inst
				instanceIPs[inst.IncusName()] = ips

				changed = true
			case ActionStop:
				delete(instances, inst.IncusName())
				delete(instanceIPs, inst.IncusName())

				changed = true
			}

			if !changed {
				return err
			}

			owned := make([]string, 0, len(ownedSet))
			for svc := range ownedSet {
				owned = append(owned, svc)
			}

			var errs error
			for _, network := range networks {
				servicesIPs := map[string][]string{}
				for instIncusName, iIPs := range instanceIPs {
					sName := instances[instIncusName].ServiceName()

					iPs := []string{}
					for _, ip := range iIPs {
						if ip.Network != network.IncusName() {
							continue
						}

						if len(ip.IPv4s) > 0 {
							iPs = append(iPs, ip.IPv4s...)
						}

						if len(ip.IPv6s) > 0 {
							iPs = append(iPs, ip.IPv6s...)
						}
					}

					if len(iPs) > 0 {
						// Aggregate by service name so every replica of a
						// service is registered under one DNS record.
						servicesIPs[sName] = append(servicesIPs[sName], iPs...)
					}
				}

				errs = errors.Join(errs, network.UpdateDNSAliases(owned, servicesIPs))
			}
			return errors.Join(err, errs)
		}

		return err
	})

	return nil
}
