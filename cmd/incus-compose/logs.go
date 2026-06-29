package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	incusApi "github.com/lxc/incus/v7/shared/api"
	"github.com/urfave/cli/v3"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
)

// ANSI color codes for log output.
var logColors = []string{
	"36",   // cyan
	"33",   // yellow
	"32",   // green
	"35",   // magenta
	"34",   // blue
	"36;1", // intense cyan
	"33;1", // intense yellow
	"32;1", // intense green
	"35;1", // intense magenta
	"34;1", // intense blue
}

// logHandler handles formatting, output of log lines, and per-instance log goroutine tracking.
type logHandler struct {
	mu         sync.Mutex
	out        io.Writer
	colors     map[string]string // resource name -> color code
	colorIndex int
	maxWidth   int
	noColor    bool
	buffers    map[string]*bytes.Buffer // resource name -> line buffer
	cancels    sync.Map                 // incus name -> context.CancelFunc
}

// newLogFormatter creates a new log formatter.
func newLogFormatter(out io.Writer, noColor bool) *logHandler {
	return &logHandler{
		out:     out,
		colors:  make(map[string]string),
		buffers: make(map[string]*bytes.Buffer),
		noColor: noColor,
	}
}

// registerService registers a service and assigns it a color.
func (f *logHandler) registerService(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.colors[name]; ok {
		return
	}

	f.colors[name] = logColors[f.colorIndex%len(logColors)]
	f.colorIndex++
	f.buffers[name] = &bytes.Buffer{}

	if len(name) > f.maxWidth {
		f.maxWidth = len(name)
	}
}

// write handles incoming log data from a resource.
func (f *logHandler) write(_ client.Action, r client.Resource, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()

	name := r.Name()

	if _, ok := f.colors[name]; !ok {
		f.colors[name] = logColors[f.colorIndex%len(logColors)]
		f.colorIndex++
		f.buffers[name] = &bytes.Buffer{}
		if len(name) > f.maxWidth {
			f.maxWidth = len(name)
		}
	}

	buf := f.buffers[name]
	buf.Write(data)

	for {
		line, err := buf.ReadBytes('\n')
		if err != nil {
			buf.Write(line)
			break
		}

		f.writeLine(name, line)
	}
}

// writeLine outputs a single line with prefix and color.
func (f *logHandler) writeLine(name string, line []byte) {
	prefix := fmt.Sprintf("%-*s | ", f.maxWidth, name)

	if f.noColor {
		_, _ = fmt.Fprintf(f.out, "%s%s", prefix, line)
	} else {
		color := f.colors[name]
		_, _ = fmt.Fprintf(f.out, "\033[%sm%s\033[0m%s", color, prefix, line)
	}
}

// flush outputs any remaining buffered data.
func (f *logHandler) flush() {
	f.mu.Lock()
	defer f.mu.Unlock()

	for name, buf := range f.buffers {
		if buf.Len() > 0 {
			line := buf.Bytes()
			f.writeLine(name, append(line, '\n'))
			buf.Reset()
		}
	}
}

// startStream begins streaming logs for an instance in a background goroutine.
func (f *logHandler) startStream(ctx context.Context, inst *client.Instance) {
	name := inst.IncusName()
	if _, running := f.cancels.Load(name); running {
		return
	}

	f.registerService(inst.Name())

	logCtx, cancel := context.WithCancel(ctx)
	f.cancels.Store(name, cancel)

	go func() {
		_ = client.RunAction(logCtx, inst, client.ActionLog, client.OptionFollow())
		f.cancels.Delete(name)
	}()
}

// stopStream cancels the log goroutine for the named instance.
func (f *logHandler) stopStream(incusName string) {
	v, ok := f.cancels.LoadAndDelete(incusName)
	if !ok {
		return
	}

	v.(context.CancelFunc)()
}

// stopStreams cancels all running log goroutines.
func (f *logHandler) stopStreams() {
	f.cancels.Range(func(key, value any) bool {
		value.(context.CancelFunc)()
		f.cancels.Delete(key)
		return true
	})
}

func newLogsCommand() *cli.Command {
	return &cli.Command{
		Name:      "logs",
		Usage:     "View output from containers",
		Category:  "compose",
		ArgsUsage: "[SERVICE...]",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:    "follow",
				Aliases: []string{"f"},
				Usage:   "Follow log output",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			noColor := noColor(ctx)

			globalClient, err := clientFromContext(ctx)
			if err != nil {
				return err
			}
			if err := globalClient.Connect(); err != nil {
				return err
			}

			p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
			if err != nil {
				globalClient.LogError("Configuring the project", "error", err)
				return errLogged.Wrap(err)
			}

			c, err := globalClient.EnsureProject(p.Name, client.EnsureProjectWithCreate())
			if err != nil {
				globalClient.LogError("Getting the incus project", "error", err)
				return errLogged.Wrap(err)
			}
			if err := c.Open(); err != nil {
				globalClient.LogError("Opening the project client", "error", err)
				return errLogged.Wrap(err)
			}
			defer func() { _ = c.Done() }()

			formatter := newLogFormatter(cmd.Root().Writer, noColor)
			globalClient.SetOutputHandler(formatter.write)

			knownNames := p.InstanceNames()
			knownInstances := make(map[string]*client.Instance, len(knownNames))
			for _, name := range knownNames {
				r, err := c.Resource(client.KindInstance, name, &client.InstanceConfig{})
				if err != nil {
					continue
				}

				inst, ok := r.(*client.Instance)
				if !ok {
					continue
				}

				knownInstances[inst.IncusName()] = inst
			}

			if !cmd.Bool("follow") {
				for _, inst := range knownInstances {
					formatter.registerService(inst.Name())
					_ = inst.Log(ctx)
				}

				formatter.flush()
				return nil
			}

			conn, err := c.Connection()
			if err != nil {
				c.LogError("Getting connection for events", "error", err)
				return errLogged.Wrap(err)
			}

			listener, err := conn.GetEventsByType([]string{incusApi.EventTypeLifecycle})
			if err != nil {
				c.LogError("Subscribing to events", "error", err)
				return errLogged.Wrap(err)
			}
			defer listener.Disconnect()

			defer formatter.stopStreams()

			projectGone := make(chan struct{})
			incusProject := c.IncusProject()

			_, err = listener.AddHandler([]string{incusApi.EventTypeLifecycle}, func(event incusApi.Event) {
				var lifecycle incusApi.EventLifecycle
				if err := json.Unmarshal(event.Metadata, &lifecycle); err != nil {
					return
				}

				if lifecycle.Action == incusApi.EventLifecycleProjectDeleted && lifecycle.Name == incusProject {
					close(projectGone)
					return
				}

				inst, known := knownInstances[lifecycle.Name]
				if !known {
					return
				}

				switch lifecycle.Action {
				case incusApi.EventLifecycleInstanceStarted:
					formatter.startStream(ctx, inst)
				case incusApi.EventLifecycleInstanceStopped, incusApi.EventLifecycleInstanceDeleted, incusApi.EventLifecycleInstanceShutdown:
					formatter.stopStream(lifecycle.Name)
				}
			})
			if err != nil {
				c.LogError("Adding event handler", "error", err)
				return errLogged.Wrap(err)
			}

			for _, inst := range knownInstances {
				formatter.startStream(ctx, inst)
			}

			select {
			case <-ctx.Done():
			case <-projectGone:
				c.LogError("Project deleted")
				formatter.flush()
				return errLogged
			}

			formatter.flush()
			return nil
		},
	}
}
