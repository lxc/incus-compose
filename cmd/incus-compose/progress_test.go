package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lxc/incus-compose/client"
)

// fakeResource is a minimal client.Resource for renderer tests.
type fakeResource struct {
	name string
}

func (f fakeResource) Kind() client.Kind { return client.KindImage }
func (f fakeResource) Name() string      { return f.name }
func (f fakeResource) IncusName() string { return f.name }
func (f fakeResource) Priority() int     { return 0 }
func (f fakeResource) IsEnsured() bool   { return true }
func (f fakeResource) Created() bool     { return false }

// newTestRenderer builds a progress renderer with a fixed 40-column width.
func newTestRenderer() (*bytes.Buffer, *progressRenderer) {
	buf := &bytes.Buffer{}
	renderer := newProgressRenderer(buf, true, true)
	renderer.width = func() int { return 40 }
	return buf, renderer
}

func TestRenderTruncatesToWidth(t *testing.T) {
	t.Parallel()

	_, renderer := newTestRenderer()

	line := &progressLine{
		action:  "ensure",
		kind:    "image",
		label:   "docker.io/library/postgres:16-alpine",
		percent: -1,
		text:    strings.Repeat("x", 100),
	}

	out := renderer.render(line, 40)
	assert.Len(t, out, 40)
	assert.True(t, strings.HasSuffix(out, "~"))
}

func TestRenderPercentTruncatesToWidth(t *testing.T) {
	t.Parallel()

	_, renderer := newTestRenderer()

	line := &progressLine{
		action:  "ensure",
		kind:    "image",
		label:   "images:alpine/edge",
		percent: 42,
		text:    "rootfs: 42% (3.10MB/s)",
	}

	out := renderer.render(line, 40)
	assert.Len(t, out, 40)
}

func TestMarkStartShowsSpinner(t *testing.T) {
	t.Parallel()

	buf, renderer := newTestRenderer()
	renderer.width = func() int { return 0 } // do not truncate away the spinner

	renderer.markStart(client.ActionEnsure, fakeResource{name: "alpine"})

	out := buf.String()
	assert.Contains(t, out, "alpine")
	assert.Contains(t, out, spinFrames[0], "a freshly started line must show the spinner")
}

func TestRenderError(t *testing.T) {
	t.Parallel()

	colored := newProgressRenderer(&bytes.Buffer{}, false, true)
	line := &progressLine{action: "start", kind: "instance", label: "web", err: assert.AnError}

	full := colored.render(line, 0)
	assert.Contains(t, full, "[error: "+assert.AnError.Error()+"]")
	assert.Contains(t, full, colorRed)
}

func TestMarkErrorPlainMode(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	renderer := newProgressRenderer(buf, true, false)

	renderer.markError(client.ActionStart, fakeResource{name: "web"}, assert.AnError)

	assert.Contains(t, buf.String(), "error: "+assert.AnError.Error())
}

func TestRenderErrorTruncatesToWidth(t *testing.T) {
	t.Parallel()

	_, renderer := newTestRenderer()
	line := &progressLine{action: "start", kind: "instance", label: "web", err: assert.AnError}

	out := renderer.render(line, 40)
	assert.Len(t, out, 40)
	assert.NotContains(t, out, "\033[", "truncated error line must not carry a torn escape")
}

func TestRenderDoneSkipsColorWhenTruncated(t *testing.T) {
	t.Parallel()

	colored := newProgressRenderer(&bytes.Buffer{}, false, true)
	line := &progressLine{action: "ensure", kind: "image", label: "alpine", done: true}

	full := colored.render(line, 0)
	assert.Contains(t, full, colorGreen)

	narrow := colored.render(line, 40)
	assert.NotContains(t, narrow, "\033[")
	assert.Len(t, narrow, 40)
}

func TestDrawLinesNeverExceedWidth(t *testing.T) {
	t.Parallel()

	buf, renderer := newTestRenderer()

	renderer.handle(client.ActionEnsure, fakeResource{name: "alpine"}, client.Options{}, client.Progress{
		Percent: -1,
		Text:    strings.Repeat("status ", 30),
	})

	for _, raw := range strings.Split(buf.String(), "\n") {
		visible := strings.TrimPrefix(raw, "\r"+ansiClearEnd)
		assert.LessOrEqual(t, len(visible), 40)
	}
}

func TestBypassWritesLogAboveBlock(t *testing.T) {
	t.Parallel()

	buf, renderer := newTestRenderer()

	renderer.handle(client.ActionEnsure, fakeResource{name: "alpine"}, client.Options{}, client.Progress{
		Percent: -1,
		Text:    "pulling",
	})
	buf.Reset()

	w := renderer.bypass()
	_, err := w.Write([]byte("a log line\n"))
	require.NoError(t, err)

	out := buf.String()
	assert.True(t, strings.HasPrefix(out, ansiUp+"\r"+ansiClearDown), "must erase the block first")
	assert.Contains(t, out, "a log line\n")
	assert.Greater(t, strings.Index(out, "alpine"), strings.Index(out, "a log line"), "block must be repainted below the log line")
}

func TestBypassBuffersPartialLines(t *testing.T) {
	t.Parallel()

	buf, renderer := newTestRenderer()

	renderer.handle(client.ActionEnsure, fakeResource{name: "alpine"}, client.Options{}, client.Progress{
		Percent: -1,
		Text:    "pulling",
	})
	buf.Reset()

	w := renderer.bypass()
	_, err := w.Write([]byte("partial"))
	require.NoError(t, err)
	assert.Empty(t, buf.String())

	_, err = w.Write([]byte(" done\n"))
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "partial done\n")
}

func TestStopFlushesPartialLogLine(t *testing.T) {
	t.Parallel()

	buf, renderer := newTestRenderer()

	w := renderer.bypass()
	_, err := w.Write([]byte("trailing"))
	require.NoError(t, err)

	renderer.Stop(true)
	assert.Contains(t, buf.String(), "trailing\n")
}

func TestSwapWriterRestores(t *testing.T) {
	t.Parallel()

	first := &bytes.Buffer{}
	second := &bytes.Buffer{}
	sw := &swapWriter{w: first}

	_, err := sw.Write([]byte("one"))
	require.NoError(t, err)

	old := sw.Swap(second)
	assert.Same(t, first, old)

	_, err = sw.Write([]byte("two"))
	require.NoError(t, err)

	assert.Equal(t, "one", first.String())
	assert.Equal(t, "two", second.String())
}
