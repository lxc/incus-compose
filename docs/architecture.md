# Architecture

High-level architecture of incus-compose and how components fit together.

## Package Structure

```
incus-compose/
├── cmd/incus-compose/  # CLI entry point
├── client/             # Incus client with resources, stack, pool
└── project/            # Compose-spec to Incus translation
```

### Package Responsibilities

**cmd/incus-compose/**

- CLI and flag parsing
- Wires together client and project
- Commands: up, down, ps, config

**client/**

- High-level Incus API wrapper
- Resources: Profile, Image, Network, StorageVolume, Instance
- Stack for task collection and ordering
- WorkerPool for parallel execution
- Hooks for action interception

**project/**

- Loads Docker Compose files via compose-go
- Translates compose services to Incus resources
- Configures client resources based on compose definitions
- Handles environment variables and dependencies

### Package Dependencies

```
cmd/incus-compose
    ├── client   (creates GlobalClient, runs Stack)
    └── project  (loads compose, configures client resources)

project
    └── client   (calls client.Resource() to create resources)
```

The CLI creates a GlobalClient and loads the compose project. Then project takes over:
it reads the compose definitions and configures resources on the client. The client
owns the resources, but project drives what gets created.

This means project is not a passive loader. It actively builds the resource graph
by calling into client. The Stack returned by project contains all resources ready
for execution.

## Resource Hierarchy

```
GlobalClient
  ├── imageCache (incus-compose-images project)
  └── Client (project-scoped)
        ├── Profile
        ├── Image
        ├── Network
        ├── StorageVolume
        └── Instance
              ├── Devices (pre-creation)
              └── PostDevices (post-creation)
```

## Image Caching (3-Stage Flow)

Images go through three stages:

1. **Remote** - OCI registry (docker.io, ghcr.io)
2. **Cache** - Local `incus-compose-images` project
3. **Project** - Project-scoped copy for instance use

```
Registry ──pull──> Cache ──copy──> Project ──use──> Instance
           (slow)        (fast)
```

Benefits:

- First pull is slow (network), subsequent runs are fast (local copy)
- No registry rate limits after initial download
- Project deletion doesn't affect cache
- Each project gets isolated image copy

## Two-Phase Resource Pattern

1. **Configuration phase** - Resource created in memory

   ```go
   image, _ := client.Resource(KindImage, "docker.io/alpine", &ImageConfig{})
   image.Config.Source = imageServer  // configure
   ```

2. **Execution phase** - Resource created on Incus
   ```go
   image.Ensure(OptionCreate())  // blocks, creates on server
   ```

## Stack and WorkerPool

### Stack

Collects resources for ordered execution:

```go
stack := client.NewStack(project)
stack.Add(profile, image, network, instance)
stack.Run(ActionEnsure, OptionCreate())
```

### WorkerPool

Executes tasks in parallel:

```go
pool := client.NewWorkerPool(4)
pool.Submit(func() error { return image1.Ensure(OptionCreate()) })
pool.Submit(func() error { return image2.Ensure(OptionCreate()) })
pool.Run(PoolRunArgs{FailFast: false})
```

### Priority-Based Ordering

Resources execute by priority. Lower values run first for ensure, last for delete:

| Resource | Priority | Create Order | Delete Order |
| -------- | -------- | ------------ | ------------ |
| Project  | 256      | 1st          | Last         |
| Profile  | 512      | 2nd          | 5th          |
| Image    | 1024     | 3rd          | 4th          |
| Network  | 2048     | 4th          | 3rd          |
| Volume   | 4096     | 5th          | 2nd          |
| Instance | 8192     | Last         | 1st          |

Images in the same batch run in parallel via WorkerPool.

## Hooks

Before and after hooks intercept resource actions for logging, validation, and error modification:

```go
client.AddHookBefore(func(action Action, r Resource, args Options, err error) error {
    log.Printf("Starting %s on %s", action, r.Name())
    return err
})
```

See [Hooks](architecture/hooks.md) for details.

## Name Sanitization

### Projects

`My_Project!` -> `my-project`

### Instances

Valid DNS names, max 63 chars, long names hashed to 32 hex chars.

### Networks

Linux interface limit (13 chars), uses hash for long names:
`backend` -> `app-backend` or `ic-a1b2c3d4e5`

## Error Handling

Sentinel errors with context enrichment:

```go
var (
    ErrDisconnected       = NewError("client is not connected")
    ErrNotEnsured         = NewError("resource not ensured")
    ErrNotFound           = NewError("resource not found")
    ErrBindMountRemote    = NewError("bind mounts not supported over network connection")
    ErrDependencyNotEnsured = NewError("dependency not ensured")
)

// Usage with context
return ErrNotFound.WithResource(r).Wrap(err)
```

Check errors with `errors.Is()`:

```go
if errors.Is(err, client.ErrNotFound) {
    // handle not found
}
```

## Connection Modes

**Direct URL (testing/CI):**

```bash
export INCUS_COMPOSE_URL="https://192.168.1.100:8443"
export INCUS_COMPOSE_CERT="./certs/client.crt"
export INCUS_COMPOSE_KEY="./certs/client.key"
```

**Provided connection (for testing):**

```go
client.New(ctx, client.ClientProvideConnection(instanceServer, cacheServer))
```

## Environment Variables

- OS environment variables NOT included by default
- `.env` files can use OS variables for interpolation
- Use `--os-env` flag for Docker Compose compatibility

## Related Documentation

- [Hooks](architecture/hooks.md) - Before/after hooks for operations
- [Client Package](architecture/client/README.md) - Resources, Stack, WorkerPool
- [Instance Details](architecture/instance.md) - Pre/post devices, UID/GID shifting
- [Images](architecture/images.md) - OCI image handling and caching
- [Health Checking](architecture/healthchecking.md) - ic-healthd sidecar, config storage, restart handling
- [Getting Started](getting-started.md) - Quick start guide
- [Compose Compatibility](compose-compatibility.md) - Docker Compose support
