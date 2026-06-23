package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lxc/incus/v7/shared/units"
	"github.com/mattn/go-isatty"
	"golang.org/x/term"

	"github.com/lxc/incus-compose/client"
)

// ANSI control sequences. Color is gated on noColor; cursor movement is gated
// on the animate flag (set only for a real terminal), so piped output and
// NO_COLOR both degrade cleanly.
const (
	ansiUp        = "\033[A" // move cursor up one line
	ansiClearEnd  = "\033[K" // clear from cursor to end of line
	ansiClearDown = "\033[J" // clear from cursor to end of screen
	colorGreen    = "\033[32m"
	colorRed      = "\033[31m"
	colorReset    = "\033[0m"

	actionWidth = 8
	kindWidth   = 18
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
	err       error  // set when the action failed; rendered as an error line
	lastPlain string // last message emitted in non-animated mode (dedup)
}

// progressRenderer turns the client's progress callbacks into terminal output.
// Operations run in parallel, so handle may be called concurrently; all state
// is guarded by mu.
type progressRenderer struct {
	out     io.Writer
	noColor bool
	animate bool       // redraw in place with a spinner (real terminal only)
	width   func() int // terminal width in columns, 0 disables truncation

	mu      sync.Mutex
	order   []string
	lines   map[string]*progressLine
	spin    int
	drawn   int    // lines drawn in the last frame (animate mode)
	logBuf  []byte // partial log line buffered by the bypass writer
	stopped bool
	stopCh  chan struct{}
	wg      sync.WaitGroup
}

// startProgress attaches a live progress renderer to the project client and
// returns a finish func that detaches it and flushes the final frame. Call it
// after any resolution-only ensure so resource lookups stay silent; only the
// wrapped actions report. The before-hook starts a spinner at action start and
// the after-hook ends it (done or error), so each action reports in
// batch/priority order.
func startProgress(globalClient *client.GlobalClient, c *client.Client, noColor bool, writer io.Writer) func(success bool) {
	if writer == nil {
		writer = os.Stderr
	}

	renderer := newProgressRenderer(writer, noColor, isatty.IsTerminal(os.Stderr.Fd()))
	renderer.Start()
	globalClient.SetProgressHandler(renderer.handle)

	// Route log records through the renderer while the live block is on
	// screen, so they print above it instead of being overwritten by the
	// next repaint. Plain mode has no in-place block, nothing to protect.
	var prevLog io.Writer
	if renderer.animate {
		prevLog = logWriter.Swap(renderer.bypass())
	}

	c.AddHookBefore(func(_ context.Context, action client.Action, r client.Resource, _ client.Options, herr error) error {
		renderer.markStart(action, r)
		return herr
	})

	c.AddHookAfter(func(_ context.Context, action client.Action, r client.Resource, _ client.Options, herr error) error {
		if herr != nil {
			renderer.markError(action, r, herr)
		} else {
			renderer.markDone(action, r)
		}
		return herr
	})

	return func(success bool) {
		globalClient.SetProgressHandler(nil)
		renderer.Stop(success)
		if prevLog != nil {
			logWriter.Swap(prevLog)
		}
	}
}

func newProgressRenderer(out io.Writer, noColor, animate bool) *progressRenderer {
	return &progressRenderer{
		out:     out,
		noColor: noColor,
		animate: animate,
		width:   termWidth,
		lines:   map[string]*progressLine{},
		stopCh:  make(chan struct{}),
	}
}

// termWidth returns the terminal width of stderr, or 0 when unknown.
func termWidth() int {
	w, _, err := term.GetSize(int(os.Stderr.Fd()))
	if err != nil {
		return 0
	}
	return w
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

	// Flush a trailing partial log line so it is not lost (bypass writer
	// buffers until a newline arrives).
	if len(p.logBuf) > 0 {
		p.writeAbove(append(p.logBuf, '\n'))
		p.logBuf = nil
	}

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

// markStart creates the line for an action/resource so a spinner shows while
// the action runs. Driven by the client's before-hook, it fires for every
// action, including quick ones (start, stop, delete) that report no progress.
func (p *progressRenderer) markStart(action client.Action, r client.Resource) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stopped {
		return
	}

	p.line(action, r)

	// Plain mode has no spinner and the fresh line carries no status text
	// yet, so there is nothing to emit until done or error.
	if p.animate {
		p.draw()
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

// markError records the failure for an action/resource so the line renders as
// an error. Driven by the client's after-hook when the action returns an error.
func (p *progressRenderer) markError(action client.Action, r client.Resource, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stopped {
		return
	}

	line := p.line(action, r)
	line.err = err

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

	width := p.width()

	var b strings.Builder
	for range p.drawn {
		b.WriteString(ansiUp)
	}
	for _, key := range p.order {
		b.WriteString("\r")
		b.WriteString(ansiClearEnd)
		b.WriteString(p.render(p.lines[key], width))
		b.WriteString("\n")
	}
	p.drawn = len(p.order)

	_, _ = io.WriteString(p.out, b.String())
}

// writeAbove erases the live block, writes raw bytes in its place, and
// repaints the block below them, so interleaved output scrolls up naturally
// (animate mode, mu held).
func (p *progressRenderer) writeAbove(b []byte) {
	var sb strings.Builder
	for range p.drawn {
		sb.WriteString(ansiUp)
	}
	sb.WriteString("\r")
	sb.WriteString(ansiClearDown)
	sb.Write(b)
	p.drawn = 0

	_, _ = io.WriteString(p.out, sb.String())
	p.draw()
}

// drawPlain emits one line per distinct status change (non-animate mode, mu held).
func (p *progressRenderer) drawPlain(line *progressLine) {
	msg := line.text
	if line.done {
		msg = "done"
	}
	if line.err != nil {
		msg = "error: " + line.err.Error()
	}
	if msg == "" || msg == line.lastPlain {
		return
	}
	line.lastPlain = msg
	_, _ = fmt.Fprintf(p.out, "%s %s %s: %s\n", line.action, line.kind, line.label, msg)
}

// render formats one line, truncated to width so it never wraps; a wrapped
// line spans two terminal rows and breaks the cursor-up repositioning in
// draw, leaving stale copies of the block behind.
func (p *progressRenderer) render(line *progressLine, width int) string {
	action := fmt.Sprintf("%-*s", actionWidth, truncate(line.action, actionWidth))
	kind := fmt.Sprintf("%-*s", kindWidth, truncate(line.kind, kindWidth))
	label := fmt.Sprintf("%-*s", labelWidth, truncate(line.label, labelWidth))

	switch {
	case line.err != nil:
		// Colorize only when the line fits; truncating would cut the
		// escape sequence and print garbage.
		status := "[error: " + line.err.Error() + "]"
		plain := action + " " + kind + " " + label + " " + status
		if width > 0 && len(plain) > width {
			return truncate(plain, width)
		}
		return action + " " + kind + " " + label + " " + p.colorize(status, colorRed)
	case line.done:
		// Colorize only when the line fits; truncating would cut the
		// escape sequence and print garbage.
		plain := action + " " + kind + " " + label + " [done]"
		if width > 0 && len(plain) > width {
			return truncate(plain, width)
		}
		return action + " " + kind + " " + label + " " + p.colorize("[done]", colorGreen)
	case line.percent >= 0:
		// Native images: text carries the transfer speed, append it.
		out := fmt.Sprintf("%s %s %s %s %3d%%", action, kind, label, bar(line.percent), line.percent)
		if line.text != "" {
			out += "  " + line.text
		}
		return fit(out, width)
	default:
		// OCI pulls: no percentage, only status text plus a spinner.
		return fit(fmt.Sprintf("%s %s %s %s %s", action, kind, label, spinFrames[p.spin%len(spinFrames)], line.text), width)
	}
}

// fit truncates s to the terminal width, no-op when the width is unknown.
func fit(s string, width int) string {
	if width > 0 && len(s) > width {
		return truncate(s, width)
	}
	return s
}

// bypass returns a writer that prints whole lines above the live progress
// block. Partial lines are buffered until their newline arrives so a torn
// write cannot split the block.
func (p *progressRenderer) bypass() io.Writer {
	return &bypassWriter{p: p}
}

type bypassWriter struct {
	p *progressRenderer
}

func (w *bypassWriter) Write(b []byte) (int, error) {
	p := w.p
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stopped {
		return p.out.Write(b)
	}

	p.logBuf = append(p.logBuf, b...)
	idx := bytes.LastIndexByte(p.logBuf, '\n')
	if idx < 0 {
		return len(b), nil
	}

	p.writeAbove(p.logBuf[:idx+1])
	p.logBuf = p.logBuf[idx+1:]
	return len(b), nil
}

// swapWriter is an io.Writer whose destination can be swapped at runtime. The
// slog handler writes through it (see initLogger) so startProgress can reroute
// log lines above the live progress block while one is on screen.
type swapWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// logWriter is the destination behind the default slog handler.
var logWriter = &swapWriter{w: os.Stderr}

func (s *swapWriter) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(b)
}

// Swap replaces the destination and returns the previous one.
func (s *swapWriter) Swap(w io.Writer) io.Writer {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.w
	s.w = w
	return old
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
