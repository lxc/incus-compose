package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"text/tabwriter"

	"github.com/urfave/cli/v3"

	"gitlab.com/r3j0/incus-compose/client"
	"gitlab.com/r3j0/incus-compose/project"
)

// psCommand implements `incus-compose ps`
// Mirrors docker-compose ps semantics (instances-only, -a, -q, --services, format table/json).
var psCommand = &cli.Command{
	Name:      "ps",
	Usage:     "List containers (instances)",
	Category:  "project",
	ArgsUsage: "[SERVICE...]",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:    "all",
			Aliases: []string{"a"},
			Usage:   "Show all containers (including stopped ones)",
		},
		&cli.BoolFlag{
			Name:    "quiet",
			Aliases: []string{"q"},
			Usage:   "Only display Incus instance names",
		},
		&cli.BoolFlag{
			Name:  "services",
			Usage: "Display services (compose service names) instead of instances",
		},
		&cli.StringFlag{
			Name:  "format",
			Usage: "Format the output. Values: [table | json]",
			Value: "table",
			Action: func(ctx context.Context, cmd *cli.Command, v string) error {
				if !slices.Contains([]string{"table", "json"}, v) {
					return fmt.Errorf("invalid format: %s (must be table or json)", v)
				}
				return nil
			},
		},
		&cli.StringFlag{
			Name:  "remote",
			Usage: "Incus remote to use",
			Value: "local",
		},
	},
	Action: func(ctx context.Context, cmd *cli.Command) error {
		globalClient, err := clientFromContext(ctx)
		if err != nil {
			return err
		}

		// Load compose project.
		p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
		if err != nil {
			globalClient.LogError("Configuring the project", "error", err)
			return errLogged.Wrap(err)
		}

		// Ensure project client exists (do not create)
		c, err := globalClient.EnsureProject(p.Name, false)
		if err != nil {
			globalClient.LogError("Getting the incus project", "error", err)
			return errLogged.Wrap(err)
		}
		if err := c.Open(); err != nil {
			globalClient.LogError("Opening the project client", "error", err)
			return errLogged.Wrap(err)
		}
		defer func() { _ = c.Close() }()

		// Build stack for the services we're interested in (only services).
		stack := client.NewStack(c)
		if err := p.ToStack(c, stack, project.ToStackOnlyServices(cmd.Args().Slice()), project.ToStackFull()); err != nil {
			c.LogError(err.Error())
			return errLogged.Wrap(err)
		}

		// Run ensure (without create) to populate resource metadata/state where possible.
		if err := stack.Run(client.ActionEnsure); err != nil {
			c.LogError("Ensuring the stack", "error", err)
			return errLogged.Wrap(err)
		}

		// Collect instance statuses.
		type psEntry struct {
			Service   string   `json:"service,omitempty"`
			Name      string   `json:"name,omitempty"`       // compose resource name
			IncusName string   `json:"incus_name,omitempty"` // actual incus instance name
			Image     string   `json:"image,omitempty"`
			Status    string   `json:"status,omitempty"`
			Addresses []string `json:"addresses,omitempty"`
		}

		entries := []psEntry{}

		// Helper to add entry if it matches filters (-a and default-running)
		addIfMatches := func(e psEntry) {
			// By default omit non-running unless --all
			if !cmd.Bool("all") && e.Status != "Running" {
				return
			}
			entries = append(entries, e)
		}

		seenServices := map[string]struct{}{}

		for _, r := range stack.All() {
			if r == nil {
				c.LogDebug("Found a nil resource")
				continue
			}

			if r.Kind() != client.KindInstance {
				// ps only lists instances
				continue
			}

			status := "Unknown"
			if r.IsEnsured() {
				status = "Exists"
			}

			incusName := r.IncusName()

			// Default entry with minimal info. We'll try to fill from Instance resource if available.
			entry := psEntry{
				Service:   r.Name(),
				Name:      r.Name(),
				IncusName: incusName,
				Image:     "",
				Status:    status,
				Addresses: []string{},
			}

			// If resource is an Instance resource and has full details, use them.
			if inst, ok := r.(*client.Instance); ok && r.IsEnsured() && inst.HasFull() {
				full := inst.IncusInstanceFull
				if full == nil || full.State == nil {
					continue
				}

				if full.Config["user.internal"] == "true" {
					continue
				}

				entry.Status = full.State.Status
				entry.Image = ""
				if inst.IncusImageAlias != nil {
					entry.Image = inst.IncusImageAlias.Name
				}
				// collect addresses
				for _, nw := range full.State.Network {
					for _, a := range nw.Addresses {
						if a.Family == "inet" && a.Scope == "global" {
							entry.Addresses = append(entry.Addresses, a.Address)
						}
					}
				}
			}

			addIfMatches(entry)
			if cmd.Bool("services") {
				seenServices[entry.Service] = struct{}{}
			}
		}

		// Include orphaned instances (instances present in the Incus project but not defined in compose).
		// Use GetInstancesFull to obtain complete instance information and avoid reflection workarounds.
		func() {
			incus := c.Connection()
			if incus == nil {
				return
			}

			instances, err := incus.GetInstancesFull("")
			if err != nil {
				// Non-fatal: if we cannot list instances, skip orphan inclusion.
				c.LogDebug("Listing instances for orphans failed", "error", err)
				return
			}

			type instMinimal struct {
				Name   string
				Status string
			}
			orphanMap := map[string]instMinimal{}

			for _, inst := range instances {
				name := inst.Name
				status := "Unknown"

				if inst.Config["user.internal"] == "true" {
					continue
				}

				if inst.State != nil && inst.State.Status != "" {
					status = inst.State.Status
				}
				orphanMap[name] = instMinimal{Name: name, Status: status}
			}

			// Remove instances that are present in stack
			for _, r := range stack.All() {
				if r == nil {
					continue
				}
				if r.Kind() != client.KindInstance {
					continue
				}
				delete(orphanMap, r.IncusName())
			}

			// Add orphans to entries
			for _, o := range orphanMap {
				e := psEntry{
					Service:   "<orphan>",
					Name:      "<orphan>",
					IncusName: o.Name,
					Image:     "",
					Status:    o.Status,
					Addresses: []string{},
				}
				addIfMatches(e)
				if cmd.Bool("services") {
					seenServices[e.Service] = struct{}{}
				}
			}
		}()

		// Handle quiet and services flags
		w := cmd.Writer
		if w == nil {
			w = os.Stdout
		}

		// If --services: print deduped service names (respecting -a filter)
		if cmd.Bool("services") {
			services := []string{}
			for s := range seenServices {
				services = append(services, s)
			}
			// Ensure stable order (sort by name)
			slices.Sort(services)
			for _, s := range services {
				_, _ = fmt.Fprintln(w, s)
			}
			return nil
		}

		// If quiet: print Incus instance names only
		if cmd.Bool("quiet") {
			for _, e := range entries {
				_, _ = fmt.Fprintln(w, e.IncusName)
			}
			return nil
		}

		// Output formatting
		switch cmd.String("format") {
		case "table":
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "NAME\tSERVICE\tINCUS_NAME\tIMAGE\tSTATUS\tADDRESSES")
			for _, e := range entries {
				addrs := ""
				if len(e.Addresses) > 0 {
					addrs = e.Addresses[0]
					for _, a := range e.Addresses[1:] {
						addrs += ", " + a
					}
				}
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
					e.Name,
					e.Service,
					e.IncusName,
					e.Image,
					e.Status,
					addrs,
				)
			}
			_ = tw.Flush()
			return nil
		case "json":
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			return enc.Encode(entries)
		default:
			// should never happen due to flag validation
			return errLogged.Wrap(errors.New("invalid format"))
		}
	},
}
