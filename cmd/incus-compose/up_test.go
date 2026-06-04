package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/suite"
	"github.com/urfave/cli/v3"

	"gitlab.com/r3j0/incus-compose/cmd/incus-compose/version"
)

type UpCommandSuite struct {
	suite.Suite
}

func (s *UpCommandSuite) TestVersionCommand() {
	oldVersion := version.Version
	version.Version = "v1.2.3"
	defer func() { version.Version = oldVersion }()

	out := &bytes.Buffer{}
	s.Require().NoError(versionCommand.Action(s.T().Context(), &cli.Command{Writer: out}))
	s.Equal("incus-compose version v1.2.3\n", out.String())
}

func (s *UpCommandSuite) TestResolveHealthdImage() {
	oldVersion := version.Version
	version.Version = "v1.2.3"
	defer func() { version.Version = oldVersion }()

	s.Equal(
		"registry.gitlab.com/r3j0/incus-compose/ic-healthd:v1.2.3",
		resolveHealthdImage("registry.gitlab.com/r3j0/incus-compose/ic-healthd:{version}"),
	)
	s.Equal("custom:latest", resolveHealthdImage("custom:latest"))
}

func (s *UpCommandSuite) TestParseScale() {
	tests := []struct {
		name   string
		values []string
		want   map[string]int
	}{
		{name: "empty", values: nil, want: map[string]int{}},
		{name: "single", values: []string{"web=3"}, want: map[string]int{"web": 3}},
		{name: "multiple", values: []string{"web=3", "api=2"}, want: map[string]int{"web": 3, "api": 2}},
		{name: "invalid ignored", values: []string{"web", "api=bad", "db=1"}, want: map[string]int{"db": 1}},
		{name: "last wins", values: []string{"web=2", "web=4"}, want: map[string]int{"web": 4}},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.Equal(tt.want, parseScale(tt.values))
		})
	}
}

func TestUpCommandSuite(t *testing.T) {
	suite.Run(t, new(UpCommandSuite))
}
