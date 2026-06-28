# Documentation

## Using incus-compose

- [Getting Started](getting-started.md) - Install and run your first compose project
- [Terminology](terms.md) - Compose vs Incus vs incus-compose terms (service vs instance, ...)
- [CLI Reference](cli.md) - All commands and options
- [Compose Compatibility](compose-compatibility.md) - Supported features and differences
- [Builds](build.md) - Build service images from Compose `build:` definitions
- [Health Checking](healthd.md) - Healthchecks, restart policies, and `service_healthy` dependencies via the ic-healthd sidecar (a core component; includes [debugging](healthd.md#debugging-ic-healthd))
- [Environment Variables](environment-variables.md) - How env vars and interpolation work
- [Why Incus?](why-incus.md) - Benefits over Docker

## Contributing and Internals

- [Contributing](../CONTRIBUTING.md) - Coding, style, and workflow rules
- [Architecture](architecture.md) - Resource-first design, layers, x-incus extensions
- [Client Package](architecture/client/README.md) - Resources, Stack, WorkerPool, hooks
- [Testing](architecture/testing.md) - just commands, test patterns, fixtures
- [GitHub Actions Runner](github-runner.md) - Set up a self-hosted runner for the test suite
