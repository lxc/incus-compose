package main

import (
	"context"
	"time"

	"github.com/urfave/cli/v3"
)

func newRestartCommand() *cli.Command {
	return &cli.Command{
		Name:      "restart",
		Usage:     "Restart running services",
		Category:  "compose",
		ArgsUsage: "[SERVICE...]",
		Flags: []cli.Flag{
			&cli.DurationFlag{
				Name:  "timeout",
				Usage: "Timeout for stopping and starting",
				Value: 1 * time.Minute,
			},
			&cli.BoolFlag{
				Name:  "with-deps",
				Usage: "Also restart linked services",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			start := newStartCommand()
			stop := newStopCommand()

			_ = stop.Action(ctx, cmd)
			return start.Action(ctx, cmd)
		},
	}
}
