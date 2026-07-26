package examples

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bradleyjkemp/cupaloy/v2"
	"github.com/stretchr/testify/require"
)

var snapshotter = cupaloy.New(cupaloy.SnapshotSubdirectory(filepath.Join("..", "test", "snapshots", "examples")))

func skipExamples(t *testing.T) {
	_, ok := os.LookupEnv("INCUS_COMPOSE_TEST_EXAMPLES")
	if !ok {
		t.Skip("Skipping: env INCUS_COMPOSE_TEST_EXAMPLES is not set, run `just test-examples` for this test")
	}
}

func runCommand(ctx context.Context, t *testing.T, projectName string, args ...string) (*bytes.Buffer, error) {
	t.Helper()

	projectName = strings.ToLower(strings.ReplaceAll(projectName, "/", "-"))

	mArgs := []string{"run", "--", "github.com/lxc/incus-compose/cmd/incus-compose/...", "--debug", "--project-name", projectName}
	mArgs = append(mArgs, args...)
	slog.DebugContext(ctx, "Running", "args", mArgs)

	stdout := &bytes.Buffer{}
	execCmd := exec.CommandContext(ctx, "go", mArgs...) //nolint:gosec
	execCmd.Stdout = stdout
	execCmd.Stderr = os.Stderr

	err := execCmd.Run()
	return stdout, err
}

// stripOutput removes dynamic content (IP addresses, network hashes) for snapshot comparison.
func stripOutput(t *testing.T, output *bytes.Buffer) string {
	t.Helper()

	ipv4Regex := regexp.MustCompile(`\d+\.\d+\.\d+\.\d+`)
	ipv6Regex := regexp.MustCompile(`(?:[0-9a-fA-F]{0,4}:){2,7}[0-9a-fA-F]{0,4}`)
	healthdImageRegex := regexp.MustCompile("ic-healthd:[0-9a-z.-]+")
	outStr := ipv4Regex.ReplaceAllString(output.String(), "-stripped-")
	outStr = ipv6Regex.ReplaceAllString(outStr, "-stripped-")
	outStr = healthdImageRegex.ReplaceAllString(outStr, "ic-healthd:-stripped-")

	// Cupaloy adds a newline, 2 lines are bad for my editors format on save.
	return strings.Trim(outStr, "\n")
}

// func TestMain(m *testing.M) {
// 	logger := slog.New(slog.NewTextHandler(
// 		os.Stderr,
// 		&slog.HandlerOptions{Level: slog.LevelDebug - 4}),
// 	)

// 	slog.SetDefault(logger)

// 	code := m.Run()
// 	os.Exit(code)
// }

func TestExample(t *testing.T) {
	t.Parallel()
	skipExamples(t)

	examples := []struct {
		name string
		dir  string
	}{
		{
			name: "hugo",
			dir:  "./hugo/",
		},
		{
			name: "leafwiki",
			dir:  "./leafwiki/",
		},
		{
			name: "immich",
			dir:  "./immich/",
		},
		{
			name: "many-dependencies",
			dir:  "./many-dependencies/",
		},
		{
			name: "wikijs",
			dir:  "./wikijs/",
		},
	}

	for _, example := range examples {
		t.Run(example.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			t.Cleanup(func() {
				_, _ = runCommand(context.Background(), t, t.Name(), "--project-directory", example.dir, "down", "--project")
			})

			args := []string{"--project-directory", example.dir, "up", "--detach"}
			_, err := runCommand(ctx, t, t.Name(), args...)
			require.NoError(t, err)

			// Sometimes this is needed to get the real health status.
			time.Sleep(1 * time.Second)

			args = []string{"--project-directory", example.dir, "list", "--format", "json"}
			stdout, err := runCommand(ctx, t, t.Name(), args...)
			require.NoError(t, err)

			snapshotter.SnapshotT(t, stripOutput(t, stdout))
		})
	}
}
