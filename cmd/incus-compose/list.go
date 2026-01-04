package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"text/tabwriter"

	"github.com/urfave/cli/v3"
	"go.yaml.in/yaml/v4"

	"gitlab.com/r3j0/incuscompose/client"
	"gitlab.com/r3j0/incuscompose/project"
)

// ProjectStatus holds the status of the project for ps output.
type ProjectStatus struct {
	Kind        string   `json:"kind" yaml:"kind"`
	Name        string   `json:"name" yaml:"name"`
	Description string   `json:"description" yaml:"description"`
	IncusName   string   `json:"incus_name" yaml:"incus_name"`
	Image       string   `json:"image" yaml:"image"`
	Status      string   `json:"status" yaml:"status"`
	Addresses   []string `json:"addresses" yaml:"addresses"`
}

// ContainerStatuses collects and formats instance statuses.
type ContainerStatuses struct {
	Writer io.Writer

	Statuses []ProjectStatus
}

// NewContainerStatuses creates a new ContainerStatuses collector.
func NewContainerStatuses(w io.Writer) *ContainerStatuses {
	return &ContainerStatuses{Writer: w, Statuses: []ProjectStatus{}}
}

// Add appends a status to the collection.
func (c *ContainerStatuses) Add(status ProjectStatus) {
	c.Statuses = append(c.Statuses, status)
}

// Yaml outputs statuses as YAML.
func (c *ContainerStatuses) Yaml() error {
	return yaml.NewEncoder(c.Writer).Encode(c.Statuses)
}

// JSON outputs statuses as JSON.
func (c *ContainerStatuses) JSON() error {
	return json.NewEncoder(c.Writer).Encode(c.Statuses)
}

// Table outputs statuses as a formatted table.
func (c *ContainerStatuses) Table() error {
	tw := tabwriter.NewWriter(c.Writer, 0, 0, 2, ' ', 0)
	_, err := fmt.Fprintln(tw, "KIND\tNAME\tINCUSNAME\tIMAGE\tSTATUS\tADDRESSES")
	if err != nil {
		return err
	}

	var errs error
	for _, s := range c.Statuses {
		addrs := ""
		if len(s.Addresses) > 0 {
			addrs = s.Addresses[0]
			for _, a := range s.Addresses[1:] {
				addrs += ", " + a
			}
		}
		_, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			s.Kind,
			s.Name,
			s.IncusName,
			s.Image,
			s.Status,
			addrs,
		)

		errs = errors.Join(errs, err)
	}

	return errors.Join(errs, tw.Flush())
}

var listCommand = &cli.Command{
	Name:      "list",
	Usage:     "List resources",
	Category:  "extensions",
	ArgsUsage: "[SERVICE...]",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "format",
			Usage: "Format the output. Values: [table | yaml | json]",
			Value: "table",
			Action: func(ctx context.Context, cmd *cli.Command, v string) error {
				if !slices.Contains([]string{"table", "yaml", "json"}, v) {
					return fmt.Errorf("invalid format: %s (must be table, yaml or json)", v)
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

		p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
		if err != nil {
			globalClient.LogError("Configuring the project", "error", err)
			return errLogged.Wrap(err)
		}

		// Get the per Project client early, gives early errors if the project does not exists
		c, err := globalClient.EnsureProject(p.Name, false)
		if err != nil {
			globalClient.LogError("Getting the incus project", "error", err)
			return errLogged.Wrap(err)
		}

		stack := client.NewStack(c)
		err = p.ToStack(c, stack, project.ToStackOnlyServices(cmd.Args().Slice()), project.ToStackFull())
		if err != nil {
			c.LogError(err.Error())
			return errLogged.Wrap(err)
		}

		err = stack.Run(client.ActionEnsure)
		if err != nil {
			c.LogError(err.Error())
			return errLogged.Wrap(err)
		}

		var rErr error

		fd := os.Stdout
		if cmd.String("output") != "" {
			fd, err = os.OpenFile(cmd.String("output"), os.O_APPEND|os.O_CREATE, 0o644)
			if err != nil {
				c.LogError(err.Error())
				return errLogged.Wrap(err)
			}

			defer fd.Close()
		}

		statuses := NewContainerStatuses(fd)

		for _, r := range stack.All() {
			if r == nil {
				c.LogDebug("Found a nil resource")
				continue
			}

			s := "not existing"
			if r.IsEnsured() {
				s = "exists"
			}

			status := ProjectStatus{
				Kind:        string(r.Kind()),
				Name:        r.Name(),
				Description: "",
				IncusName:   r.IncusName(),
				Image:       "",
				Status:      s,
				Addresses:   []string{},
			}

			if r.Kind() == client.KindInstance {
				if !r.IsEnsured() {
					continue
				}

				instance, ok := r.(*client.Instance)
				if !ok {
					err = client.ErrUnknown.WithResource(r)
					c.LogError("Getting an instance", err)
					rErr = errors.Join(rErr, err)
					continue
				}

				if !instance.HasFull() {
					c.LogDebug("Skipping cause not having a full instance", "kind", r.Kind(), "name", r.Name(), "resource", r)
					continue
				}

				instFull := instance.IncusInstanceFull
				status.IncusName = instance.IncusName()

				status.Status = instFull.State.Status
				status.Image = instance.IncusImageAlias.Name
				status.Description = instFull.Description

				// Get IP addresses
				for _, network := range instFull.State.Network {
					for _, addr := range network.Addresses {
						if addr.Family == "inet" && addr.Scope == "global" {
							status.Addresses = append(status.Addresses, addr.Address)
						}
					}
				}
			}

			statuses.Add(status)
		}

		if rErr != nil {
			return rErr
		}

		switch cmd.String("format") {
		case "table":
			err := statuses.Table()
			if err != nil {
				c.LogError(err.Error())
				return errLogged.Wrap(err)
			}
		case "yaml":
			err := statuses.Table()
			if err != nil {
				c.LogError(err.Error())
				return errLogged.Wrap(err)
			}
		case "json":
			err := statuses.Table()
			if err != nil {
				c.LogError(err.Error())
				return errLogged.Wrap(err)
			}
		default:
			// This should never happen.
			c.LogError("Unknown format", "format", cmd.String("format"))
			return errLogged.Wrap(err)
		}

		return nil
	},
}
