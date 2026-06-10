package main

import (
	"strings"
)

// TestExecSelectsCorrectInstance is a regression test for the exec command
// dispatching to the wrong instance when multiple services share a stack.
// It runs `hostname` in each service of a multi-service project and asserts
// the output matches the expected Incus instance name.
func (s *E2ESuite) TestExecSelectsCorrectInstance() {
	compose := "../../test/fixtures/nginx-proxy/compose.yaml"

	defer func() {
		_, _, _ = s.run("-f", compose, "down", "--project")
	}()

	_, _, err := s.run("-f", compose, "up", "--detach")
	s.Require().NoError(err)

	tests := []struct {
		service  string
		wantHost string
	}{
		{"nginx", "nginx-1"},
		{"backend1", "backend1-1"},
		{"backend2", "backend2-1"},
	}

	for _, tt := range tests {
		s.Run(tt.service, func() {
			stdout, _, err := s.run("-f", compose, "exec", "--no-tty", tt.service, "hostname")
			s.Require().NoError(err)
			s.Equal(tt.wantHost, strings.TrimSpace(stdout))
		})
	}
}
