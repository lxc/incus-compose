package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/lxc/incus-compose/client"
)

type FormattersSuite struct {
	suite.Suite
}

type logResource struct {
	name     string
	priority int
}

func (r logResource) Kind() client.Kind { return client.KindInstance }
func (r logResource) Name() string      { return r.name }
func (r logResource) IncusName() string { return r.name }
func (r logResource) Priority() int     { return r.priority }
func (r logResource) IsEnsured() bool   { return false }
func (r logResource) Created() bool     { return false }

func (s *FormattersSuite) TestContainerStatusesFormats() {
	status := ProjectStatus{
		Kind:      "container",
		Name:      "web",
		IncusName: "web-1",
		Image:     "docker.io/nginx:alpine",
		Status:    "Running",
		Addresses: []string{"10.0.0.2", "fd42::2"},
	}

	s.Run("table", func() {
		var buf bytes.Buffer
		statuses := NewContainerStatuses(&buf)
		statuses.Add(status)

		s.Require().NoError(statuses.Table())
		s.Contains(buf.String(), "KIND")
		s.Contains(buf.String(), "web-1")
		s.Contains(buf.String(), "10.0.0.2, fd42::2")
	})

	s.Run("json", func() {
		var buf bytes.Buffer
		statuses := NewContainerStatuses(&buf)
		statuses.Add(status)

		s.Require().NoError(statuses.JSON())
		s.Contains(buf.String(), `"name": "web"`)
		s.Contains(buf.String(), `"addresses": [`)
	})

	s.Run("yaml", func() {
		var buf bytes.Buffer
		statuses := NewContainerStatuses(&buf)
		statuses.Add(status)

		s.Require().NoError(statuses.Yaml())
		s.Contains(buf.String(), "name: web")
		s.Contains(buf.String(), "incus_name: web-1")
	})
}

func (s *FormattersSuite) TestSortResources() {
	resources := []client.Resource{
		logResource{name: "web-2", priority: client.PriorityInstance},
		logResource{name: "ic-net", priority: client.PriorityNetwork},
		logResource{name: "db-1", priority: client.PriorityInstance},
		logResource{name: "web-1", priority: client.PriorityInstance},
	}

	names := []string{}
	for _, r := range sortResources(resources) {
		names = append(names, r.IncusName())
	}

	s.Equal([]string{"ic-net", "db-1", "web-1", "web-2"}, names)
	s.Equal("web-2", resources[0].IncusName(), "input slice must stay untouched")
}

func (s *FormattersSuite) TestLogFormatterNoColor() {
	var buf bytes.Buffer
	formatter := newLogFormatter(&buf, true)

	formatter.registerService("web")
	formatter.registerService("database")
	formatter.write(client.ActionLog, logResource{name: "web"}, []byte("first\npartial"))
	formatter.write(client.ActionLog, logResource{name: "database"}, []byte("ready\n"))
	formatter.flush()

	output := buf.String()
	s.Contains(output, "web      | first\n")
	s.Contains(output, "database | ready\n")
	s.Contains(output, "web      | partial\n")
}

func (s *FormattersSuite) TestLogFormatterColor() {
	var buf bytes.Buffer
	formatter := newLogFormatter(&buf, false)

	formatter.write(client.ActionLog, logResource{name: "web"}, []byte("hello\n"))
	formatter.flush()

	output := buf.String()
	s.True(strings.Contains(output, "\033["))
	s.Contains(output, "web | ")
	s.Contains(output, "hello\n")
}

func TestFormattersSuite(t *testing.T) {
	suite.Run(t, new(FormattersSuite))
}
