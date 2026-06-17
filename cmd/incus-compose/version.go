package main

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/lxc/incus-compose/cmd/incus-compose/version"
)

func newVersionCommand() *cli.Command {
	return &cli.Command{
		Name:  "version",
		Usage: "Print version information",
		Action: func(_ context.Context, cmd *cli.Command) error {
			out := cmd.Writer
			if out == nil && cmd.Root() != nil {
				out = cmd.Root().Writer
			}
			if out == nil {
				out = os.Stdout
			}
			_, err := fmt.Fprintf(out, "incus-compose version %s\n", version.Current())
			return err
		},
	}
}
