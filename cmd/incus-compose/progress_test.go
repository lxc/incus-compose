package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

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

type ProgressSuite struct {
	suite.Suite
	buf      *bytes.Buffer
	renderer *progressRenderer
}

func TestProgressSuite(t *testing.T) {
	suite.Run(t, new(ProgressSuite))
}

func (s *ProgressSuite) SetupTest() {
	s.buf = &bytes.Buffer{}
	s.renderer = newProgressRenderer(s.buf, true, true)
	s.renderer.width = func() int { return 40 }
}

func (s *ProgressSuite) TestRenderTruncatesToWidth() {
	line := &progressLine{
		action:  "ensure",
		kind:    "image",
		label:   "docker.io/library/postgres:16-alpine",
		percent: -1,
		text:    strings.Repeat("x", 100),
	}

	out := s.renderer.render(line, 40)
	s.Len(out, 40)
	s.True(strings.HasSuffix(out, "~"))
}

func (s *ProgressSuite) TestRenderPercentTruncatesToWidth() {
	line := &progressLine{
		action:  "ensure",
		kind:    "image",
		label:   "images:alpine/edge",
		percent: 42,
		text:    "rootfs: 42% (3.10MB/s)",
	}

	out := s.renderer.render(line, 40)
	s.Len(out, 40)
}

func (s *ProgressSuite) TestRenderDoneSkipsColorWhenTruncated() {
	colored := newProgressRenderer(s.buf, false, true)
	line := &progressLine{action: "ensure", kind: "image", label: "alpine", done: true}

	full := colored.render(line, 0)
	s.Contains(full, colorGreen)

	narrow := colored.render(line, 40)
	s.NotContains(narrow, "\033[")
	s.Len(narrow, 40)
}

func (s *ProgressSuite) TestDrawLinesNeverExceedWidth() {
	s.renderer.handle(client.ActionEnsure, fakeResource{name: "alpine"}, client.Options{}, client.Progress{
		Percent: -1,
		Text:    strings.Repeat("status ", 30),
	})

	for _, raw := range strings.Split(s.buf.String(), "\n") {
		visible := strings.TrimPrefix(raw, "\r"+ansiClearEnd)
		s.LessOrEqual(len(visible), 40)
	}
}

func (s *ProgressSuite) TestBypassWritesLogAboveBlock() {
	s.renderer.handle(client.ActionEnsure, fakeResource{name: "alpine"}, client.Options{}, client.Progress{
		Percent: -1,
		Text:    "pulling",
	})
	s.buf.Reset()

	w := s.renderer.bypass()
	_, err := w.Write([]byte("a log line\n"))
	s.NoError(err)

	out := s.buf.String()
	s.True(strings.HasPrefix(out, ansiUp+"\r"+ansiClearDown), "must erase the block first")
	s.Contains(out, "a log line\n")
	s.Greater(strings.Index(out, "alpine"), strings.Index(out, "a log line"), "block must be repainted below the log line")
}

func (s *ProgressSuite) TestBypassBuffersPartialLines() {
	s.renderer.handle(client.ActionEnsure, fakeResource{name: "alpine"}, client.Options{}, client.Progress{
		Percent: -1,
		Text:    "pulling",
	})
	s.buf.Reset()

	w := s.renderer.bypass()
	_, err := w.Write([]byte("partial"))
	s.NoError(err)
	s.Empty(s.buf.String())

	_, err = w.Write([]byte(" done\n"))
	s.NoError(err)
	s.Contains(s.buf.String(), "partial done\n")
}

func (s *ProgressSuite) TestStopFlushesPartialLogLine() {
	w := s.renderer.bypass()
	_, err := w.Write([]byte("trailing"))
	s.NoError(err)

	s.renderer.Stop(true)
	s.Contains(s.buf.String(), "trailing\n")
}

func (s *ProgressSuite) TestSwapWriterRestores() {
	first := &bytes.Buffer{}
	second := &bytes.Buffer{}
	sw := &swapWriter{w: first}

	_, err := sw.Write([]byte("one"))
	s.NoError(err)

	old := sw.Swap(second)
	s.Same(first, old)

	_, err = sw.Write([]byte("two"))
	s.NoError(err)

	s.Equal("one", first.String())
	s.Equal("two", second.String())
}
