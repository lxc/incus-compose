package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/mattn/go-colorable"
	"github.com/urfave/cli/v3"

	"gitlab.com/r3j0/incus-compose/client"
	"gitlab.com/r3j0/incus-compose/project"
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

// logFormatter handles formatting and output of log lines from multiple services.
type logFormatter struct {
	mu         sync.Mutex
	out        io.Writer
	colors     map[string]string // resource name -> color code
	colorIndex int
	maxWidth   int
	noColor    bool
	buffers    map[string]*bytes.Buffer // resource name -> line buffer
}

// newLogFormatter creates a new log formatter.
func newLogFormatter(out io.Writer, noColor bool) *logFormatter {
	return &logFormatter{
		out:      out,
		colors:   make(map[string]string),
		buffers:  make(map[string]*bytes.Buffer),
		noColor:  noColor,
		maxWidth: 0,
	}
}

// registerService registers a service and assigns it a color.
func (f *logFormatter) registerService(name string) {
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
func (f *logFormatter) write(action client.Action, r client.Resource, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()

	name := r.Name()

	// Ensure service is registered
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

	// Process complete lines
	for {
		line, err := buf.ReadBytes('\n')
		if err != nil {
			// No complete line yet, put back unprocessed data
			buf.Write(line)
			break
		}

		// Output the line with prefix
		f.writeLine(name, line)
	}
}

// writeLine outputs a single line with prefix and color.
func (f *logFormatter) writeLine(name string, line []byte) {
	prefix := fmt.Sprintf("%-*s | ", f.maxWidth, name)

	if f.noColor {
		_, _ = fmt.Fprintf(f.out, "%s%s", prefix, line)
	} else {
		color := f.colors[name]
		// Color the prefix, not the log content
		_, _ = fmt.Fprintf(f.out, "\033[%sm%s\033[0m%s", color, prefix, line)
	}
}

// flush outputs any remaining buffered data.
func (f *logFormatter) flush() {
	f.mu.Lock()
	defer f.mu.Unlock()

	for name, buf := range f.buffers {
		if buf.Len() > 0 {
			// Output remaining data even if no newline
			line := buf.Bytes()
			f.writeLine(name, append(line, '\n'))
			buf.Reset()
		}
	}
}

var logsCommand = &cli.Command{
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
		follow := cmd.Bool("follow")
		noColor := noColorFromAnsi(cmd.String("ansi"), os.Stdout.Fd())

		globalClient, err := clientFromContext(ctx)
		if err != nil {
			return err
		}

		p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
		if err != nil {
			globalClient.LogError("Configuring the project", "error", err)
			return errLogged.Wrap(err)
		}

		// Get the per Project client - don't create if it doesn't exist
		c, err := globalClient.EnsureProject(p.Name, false)
		if err != nil {
			globalClient.LogError("Getting the incus project", "error", err)
			return errLogged.Wrap(err)
		}

		// Create log formatter with colorable output
		// Use cmd.Root().Writer if it's a file, otherwise fallback to os.Stdout
		var out io.Writer
		if f, ok := cmd.Root().Writer.(*os.File); ok {
			out = colorable.NewColorable(f)
		} else {
			out = cmd.Root().Writer
		}
		formatter := newLogFormatter(out, noColor)

		// Set up output handler
		globalClient.SetOutputHandler(formatter.write)

		// Build stack with only requested services (or all)
		stack := client.NewStack(c)
		services := cmd.Args().Slice()
		err = p.ToStack(c, stack, project.ToStackOnlyServices(services))
		if err != nil {
			c.LogError(err.Error())
			return errLogged.Wrap(err)
		}

		// Ensure instances exist (fetch state)
		err = stack.ForAction(client.ActionEnsure).Run(client.ActionEnsure)
		if err != nil {
			c.LogError("Ensuring the stack", "error", err)
			return errLogged.Wrap(err)
		}

		// Pre-register services for consistent prefix width
		for _, r := range stack.ForAction(client.ActionLog).All() {
			formatter.registerService(r.Name())
		}

		// Build options for log action
		var opts []client.Option
		if follow {
			opts = append(opts, client.OptionFollow())
		}

		// Stream logs
		err = stack.ForAction(client.ActionLog).Run(client.ActionLog, opts...)
		if err != nil {
			c.LogError("Getting logs", "error", err)
			return errLogged.Wrap(err)
		}

		// Flush any remaining buffered output
		formatter.flush()

		return nil
	},
}
