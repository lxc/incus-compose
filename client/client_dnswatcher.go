package client

import (
	"errors"
	"sync"
	"time"
)

// dnsIPWaitTimeout bounds how long to wait for a freshly started instance to
// acquire its DHCP lease before recording its DNS address.
const dnsIPWaitTimeout = 15 * time.Second

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

	c.AddHookAfter(func(action Action, r Resource, _ Options, err error) error {
		if err != nil || r == nil {
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
					ips, ipErr := inst.WaitIPs(dnsIPWaitTimeout)
					if ipErr != nil {
						return ipErr
					}

					instances[inst.IncusName()] = inst
					instanceIPs[inst.IncusName()] = ips

					changed = true
				}
			case ActionStart:
				ips, ipErr := inst.WaitIPs(dnsIPWaitTimeout)
				if ipErr != nil {
					return ipErr
				}

				instances[inst.IncusName()] = inst
				instanceIPs[inst.IncusName()] = ips

				changed = true
			case ActionStop, ActionDelete:
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
						cSIPs, ok := servicesIPs[instIncusName]
						if ok {
							cSIPs := append(cSIPs, iPs...)
							servicesIPs[sName] = cSIPs
						} else {
							servicesIPs[sName] = iPs
						}
					}
				}

				c.LogDebug("DNSWatcher updating network", "network", network.Name(), "serviceIPs", servicesIPs)

				errs = errors.Join(errs, network.UpdateDNSAliases(owned, servicesIPs))
			}
			return errors.Join(err, errs)
		}

		return err
	})

	return nil
}
