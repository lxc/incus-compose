package incuscompose

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRunOptions(t *testing.T) {
	assert.Equal(t, DefaultVerbosity, NewRunOptions().Verbosity)
	assert.Equal(t, VerbosityDebug, NewRunOptions(WithVerbosity(VerbosityDebug)).Verbosity)
	assert.NotEqual(t, DefaultVerbosity, NewRunOptions(WithVerbosity(VerbosityDebug)).Verbosity)

	assert.False(t, NewRunOptions().Build)
	assert.True(t, NewRunOptions(RunBuild()).Build)

	opts := NewRunOptions(RunCmd("sh", []string{"-c", `"false;"`}))
	assert.Equal(t, "sh", opts.Cmd)
	assert.Equal(t, []string{"-c", `"false;"`}, opts.CmdArgs)
}
