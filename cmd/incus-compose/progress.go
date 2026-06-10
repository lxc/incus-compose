package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lxc/incus/v7/shared/units"
	"github.com/mattn/go-isatty"

	"gitlab.com/r3j0/incus-compose/client"
)

// ANSI control sequences. Color is gated on noColor; cursor movement is gated
// on the animate flag (set only for a real terminal), so piped output and
// NO_COLOR both degrade cleanly.
const (
	ansiUp       = "\033[A" // move cursor up one line
	ansiClearEnd = "\033[K" // clear from cursor to end of line
	colorGreen   = "\033[32m"
	colorReset   = "\033[0m"

	actionWidth = 8
	kindWith    = 18
	labelWidth  = 38
	barWidth    = 20
)

// spinFrames is an ASCII spinner; braille frames would violate the no-non-ASCII
// rule and misrender in narrow terminals.
var spinFrames = []string{"-", "\\", "|", "/"}

// progressLine is the live state of one resource action.
type progressLine struct {
	action    string
	kind      string
	label     string
	percent   int    // -1 when the operation reports no percentage (OCI pulls)
	text      string // latest status text from Incus
	done      bool
	lastPlain string // last message emitted in non-animated mode (dedup)
}

// progressRenderer turns the client's progress callbacks into terminal output.
// Operations run in parallel, so handle may be called concurrently; all state
// is guarded by mu.
type progressRenderer struct {
	out     io.Writer
	noColor bool
	animate bool // redraw in place with a spinner (real terminal only)

	mu      sync.Mutex
	order   []string
	lines   map[string]*progressLine
	spin    int
	drawn   int // lines drawn in the last frame (animate mode)
	stopped bool
	stopCh  chan struct{}
	wg      sync.WaitGroup
}

// startProgress attaches a live progress renderer to the project client and
// returns a finish func that detaches it and flushes the final frame. Call it
// after any resolution-only ensure so resource lookups stay silent; only the
// wrapped actions report. The markDone hook fires at action completion, so each
// action reports in batch/priority order.
func startProgress(globalClient *client.GlobalClient, c *client.Client, errWriter io.Writer) func(success bool) {
	if errWriter == nil {
		errWriter = os.Stderr
	}

	renderer := newProgressRenderer(errWriter, noColor, isatty.IsTerminal(os.Stderr.Fd()) && !globalClient.IsDebugging())
	renderer.Start()
	globalClient.SetProgressHandler(renderer.handle)
	c.AddHookAfter(func(_ context.Context, action client.Action, r client.Resource, _ client.Options, herr error) error {
		if herr == nil {
			renderer.markDone(action, r)
		}
		return herr
	})

	return func(success bool) {
		globalClient.SetProgressHandler(nil)
		renderer.Stop(success)
	}
}

func newProgressRenderer(out io.Writer, noColor, animate bool) *progressRenderer {
	return &progressRenderer{
		out:     out,
		noColor: noColor,
		animate: animate,
		lines:   map[string]*progressLine{},
		stopCh:  make(chan struct{}),
	}
}

// Start launches the spinner ticker in animated mode. It is a no-op otherwise.
func (p *progressRenderer) Start() {
	if !p.animate {
		return
	}

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(120 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-p.stopCh:
				return
			case <-ticker.C:
				p.mu.Lock()
				p.spin++
				p.draw()
				p.mu.Unlock()
			}
		}
	}()
}

// Stop ends rendering. On success every line is marked done so the final frame
// reads cleanly; on failure the last observed state is left in place.
func (p *progressRenderer) Stop(success bool) {
	if p.animate {
		close(p.stopCh)
		p.wg.Wait()
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.stopped = true

	if success {
		for _, key := range p.order {
			p.lines[key].done = true
		}
	}

	if p.animate {
		p.draw()
		return
	}

	for _, key := range p.order {
		p.drawPlain(p.lines[key])
	}
}

// line returns the tracked line for an action/resource pair, creating it on
// first use. Lines are keyed by action so a resource that goes through several
// actions (e.g. restart: stop then start) gets one line per action. mu must be
// held.
func (p *progressRenderer) line(action client.Action, r client.Resource) *progressLine {
	key := string(action) + "/" + r.IncusName()
	line, ok := p.lines[key]
	if !ok {
		kind, label := resourceLabel(r)
		line = &progressLine{action: string(action), kind: kind, label: label, percent: -1}
		p.lines[key] = line
		p.order = append(p.order, key)
	}
	return line
}

// handle is the client.SetProgressHandler callback.
func (p *progressRenderer) handle(action client.Action, r client.Resource, _ client.Options, prog client.Progress) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stopped {
		return
	}

	line := p.line(action, r)
	if prog.Percent >= 0 {
		line.percent = prog.Percent
	}
	if prog.Text != "" {
		line.text = prog.Text
	}

	if p.animate {
		p.draw()
	} else {
		p.drawPlain(line)
	}
}

// markDone marks an action/resource as finished. Driven by the client's
// after-hook (fires at action completion), it creates the line if no progress
// event arrived, so quick actions (start, stop, delete) still report. Batches
// run in priority order, so images report done before instances.
func (p *progressRenderer) markDone(action client.Action, r client.Resource) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stopped {
		return
	}

	line := p.line(action, r)
	if line.done {
		return
	}
	line.done = true

	if p.animate {
		p.draw()
	} else {
		p.drawPlain(line)
	}
}

// draw repaints the whole block in place (animate mode, mu held).
func (p *progressRenderer) draw() {
	if len(p.order) == 0 {
		return
	}

	var b strings.Builder
	for range p.drawn {
		b.WriteString(ansiUp)
	}
	for _, key := range p.order {
		b.WriteString("\r")
		b.WriteString(ansiClearEnd)
		b.WriteString(p.render(p.lines[key]))
		b.WriteString("\n")
	}
	p.drawn = len(p.order)

	_, _ = io.WriteString(p.out, b.String())
}

// drawPlain emits one line per distinct status change (non-animate mode, mu held).
func (p *progressRenderer) drawPlain(line *progressLine) {
	msg := line.text
	if line.done {
		msg = "done"
	}
	if msg == "" || msg == line.lastPlain {
		return
	}
	line.lastPlain = msg
	_, _ = fmt.Fprintf(p.out, "%s %s %s: %s\n", line.action, line.kind, line.label, msg)
}

func (p *progressRenderer) render(line *progressLine) string {
	action := fmt.Sprintf("%-*s", actionWidth, truncate(line.action, actionWidth))
	kind := fmt.Sprintf("%-*s", kindWith, truncate(line.kind, kindWith))
	label := fmt.Sprintf("%-*s", labelWidth, truncate(line.label, labelWidth))

	switch {
	case line.done:
		return action + " " + kind + " " + label + " " + p.colorize("[done]", colorGreen)
	case line.percent >= 0:
		// Native images: text carries the transfer speed, append it.
		out := fmt.Sprintf("%s %s %s %s %3d%%", action, kind, label, bar(line.percent), line.percent)
		if line.text != "" {
			out += "  " + line.text
		}
		return out
	default:
		// OCI pulls: no percentage, only status text plus a spinner.
		return fmt.Sprintf("%s %s %s %s %s", action, kind, label, spinFrames[p.spin%len(spinFrames)], line.text)
	}
}

func (p *progressRenderer) colorize(s, color string) string {
	if p.noColor {
		return s
	}
	return color + s + colorReset
}

// resourceLabel builds a per-resource label, appending the size when the
// resource exposes one (images resolve it before a download).
func resourceLabel(r client.Resource) (string, string) {
	if sz, ok := r.(interface{ Size() int64 }); ok && sz.Size() > 0 {
		return string(r.Kind()), fmt.Sprintf("%s (%s)", r.Name(), units.GetByteSizeString(sz.Size(), 1))
	}
	return string(r.Kind()), r.Name()
}

func bar(percent int) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := percent * barWidth / 100
	return "[" + strings.Repeat("#", filled) + strings.Repeat("-", barWidth-filled) + "]"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "~"
}
