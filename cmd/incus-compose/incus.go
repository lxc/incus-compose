package main

import (
	"context"
	"errors"
	"os"
	"os/exec"

	"github.com/urfave/cli/v3"

	"gitlab.com/r3j0/incus-compose/client"
	"gitlab.com/r3j0/incus-compose/project"
)

// incusCommand proxies arbitrary incus CLI commands into the current compose project context
// by injecting INCUS_PROJECT=<sanitized-project-name> into the environment.
var incusCommand = &cli.Command{
	Name:            "incus",
	Usage:           "Run an incus command in the current project context",
	Category:        "extensions",
	ArgsUsage:       "COMMAND [ARGS...]",
	SkipFlagParsing: true,
	Action: func(ctx context.Context, cmd *cli.Command) error {
		globalClient := client.New(ctx)

		p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
		if err != nil {
			globalClient.LogError("Loading the project", "error", err)
			return errLogged.Wrap(err)
		}

		incusProject := client.NewOfflineClient(ctx, p.Name).IncusProject()

		execPath, err := exec.LookPath("incus")
		if err != nil {
			globalClient.LogError("`incus` not found in PATH")
			return errLogged.Wrap(errors.New("'incus' not found in PATH"))
		}

		execCmd := exec.CommandContext(ctx, execPath, cmd.Args().Slice()...) //nolint:gosec
		execCmd.Stdin = os.Stdin
		execCmd.Stdout = os.Stdout
		execCmd.Stderr = os.Stderr
		execCmd.Env = append(os.Environ(), "INCUS_PROJECT="+incusProject)

		if err := execCmd.Run(); err != nil {
			return errLogged.Wrap(err)
		}
		return nil
	},
}
