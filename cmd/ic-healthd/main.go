// ic-healthd is a health check daemon for incus-compose.
// It monitors services with healthcheck directives and restarts unhealthy instances.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"

	"gitlab.com/r3j0/incus-compose/cmd/ic-healthd/version"
)

const (
	defaultDataDir    = "/var/lib/ic-healthd"
	defaultSecretsDir = "/run/secrets/ic-healthd"
)

func main() {
	app := &cli.Command{
		Name:  "ic-healthd",
		Usage: "Health check daemon for incus-compose",
		Commands: []*cli.Command{
			runCommand,
			versionCommand,
		},
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}
}

var runCommand = &cli.Command{
	Name:  "run",
	Usage: "Run the health check daemon",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "incus",
			Usage:   "URL of the incus api",
			Sources: cli.EnvVars("IC_HEALTHD_INCUS_URL"),
		},
		&cli.StringSliceFlag{
			Name:  "project",
			Usage: "projects to manage",
		}, &cli.StringFlag{
			Name:    "data-dir",
			Usage:   "Persistent volume directory containing the generated cert/key",
			Value:   defaultDataDir,
			Sources: cli.EnvVars("IC_HEALTHD_INCUS_PROJECT"),
		},
		&cli.StringFlag{
			Name:    "secrets-dir",
			Usage:   "Tmpfs directory containing the one-time registration token",
			Value:   defaultSecretsDir,
			Sources: cli.EnvVars("IC_HEALTHD_SECRETS_DIR"),
		},
		&cli.BoolFlag{
			Name:    "debug",
			Usage:   "Enable verbose logging",
			Sources: cli.EnvVars("IC_HEALTHD_DEBUG"),
		},
	},
	Before: func(ctx context.Context, _ *cli.Command) (context.Context, error) {
		for range 10 {
			if hasDefaultRoute() {
				return ctx, nil
			}
			time.Sleep(time.Second)
		}
		return ctx, nil
	},
	Action: runAction,
}

var versionCommand = &cli.Command{
	Name:  "version",
	Usage: "Print version information",
	Action: func(ctx context.Context, cmd *cli.Command) error {
		log.Printf("ic-healthd version %s", version.Current())
		return nil
	},
}

// hasDefaultRoute reports whether the kernel routing table has a default route.
// It reads /proc/net/route directly; destination 00000000 indicates a default route.
func hasDefaultRoute() bool {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n")[1:] {
		if fields := strings.Fields(line); len(fields) >= 2 && fields[1] == "00000000" {
			return true
		}
	}
	return false
}

func runAction(ctx context.Context, cmd *cli.Command) error {
	cfg := &Config{}
	cfg.DataDir = cmd.String("data-dir")
	cfg.SecretsDir = cmd.String("secrets-dir")
	cfg.Debug = cmd.Bool("debug")
	cfg.IncusURL = cmd.String("incus")
	cfg.Projects = cmd.StringSlice("project")

	log.Printf("version: %s", version.Current())

	if cfg.Debug {
		log.Println("debug mode enabled")
		log.Printf("data dir: %s", cfg.DataDir)
		log.Printf("secrets dir: %s", cfg.SecretsDir)
		log.Printf("incus: %s", cfg.IncusURL)
		log.Printf("projects: %s", cfg.Projects)
	}

	runner, err := NewRunner(cfg)
	if err != nil {
		return err
	}

	// Setup signal handling
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	reload := make(chan struct{}, 1)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigChan)

	go func() {
		for {
			select {
			case sig := <-sigChan:
				switch sig {
				case syscall.SIGHUP:
					log.Printf("received signal %v, reloading", sig)
					select {
					case reload <- struct{}{}:
					default:
						log.Println("reload already pending")
					}
				default:
					log.Printf("received signal %v, shutting down", sig)
					cancel()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Run health checks
	return runner.Run(ctx, reload)
}
