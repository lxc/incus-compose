# Client Package

The client package provides a high-level Incus API wrapper with resource management and parallel execution.

## Core Types

### GlobalClient

Entry point for all Incus operations. Manages the connection and projects:

```go
c := client.New(ctx, client.ClientLogger(logger))
err := c.Connect()

project, err := c.EnsureProject("myapp", true)
defer c.DeleteProject("myapp", true)
```

### Client

Project-scoped client for resource operations:

```go
profile, _ := project.Resource(client.KindProfile, "default", &client.ProfileConfig{})
image, _ := project.Resource(client.KindImage, "docker.io/nginx:alpine", &client.ImageConfig{})
network, _ := project.Resource(client.KindNetwork, "backend", &client.NetworkConfig{})
volume, _ := project.Resource(client.KindStorageVolume, "data", &client.StorageVolumeConfig{})
instance, _ := project.Resource(client.KindInstance, "web", &client.InstanceConfig{})
```

## Resource Interface

All resources implement the `Resource` interface:

```go
type Resource interface {
    Kind() Kind
    Name() string
    IncusName() string
    Priority() int
    IsEnsured() bool
}
```

Action interfaces are implemented as needed:

- `EnsureAble` - resources that can be created/fetched
- `DeleteAble` - resources that can be deleted
- `StartAble` - resources that can be started (Instance only)
- `StopAble` - resources that can be stopped (Instance only)

## ResourceStore

Storage for resources within a project. Uses slices for ordering:

```go
type ResourceStore struct {
    resources []Resource
}

func (s *ResourceStore) Add(r Resource)
func (s *ResourceStore) Get(kind Kind, name string) Resource
func (s *ResourceStore) All() []Resource
```

Each Client has a single ResourceStore for all resource types.

## Options

Action options configure how operations behave:

```go
type Options struct {
    Create  bool  // Create if not exists (for Ensure)
    Force   bool  // Force deletion/stop
    Timeout int   // Timeout in seconds
}
```

Use functional options:

```go
err := instance.Ensure(client.OptionCreate())
err := instance.Delete(client.OptionForce())
```

## Hooks

Hooks intercept resource operations. Both GlobalClient and Client support hooks.

### Before Hooks (FIFO)

Run before actions. Can abort by returning an error:

```go
client.AddHookBefore(func(action Action, r Resource, args Options, err error) error {
    log.Printf("Starting %s on %s", action, r.Name())
    return err
})
```

### After Hooks (LIFO)

Run after actions. Can modify or wrap errors:

```go
client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
    if err != nil {
        log.Printf("Failed %s on %s: %v", action, r.Name(), err)
    }
    return err
})
```

### Operation Hooks

Handle Incus operation progress and waiting:

```go
// hookOperation - for local operations
// hookRemoteOperation - for remote operations (image copies)
```

These are set internally and handle operation waiting and progress reporting.

## WorkerPool

Parallel task execution:

```go
pool := client.NewWorkerPool(4)

pool.Submit(func() error { return image1.Ensure(client.OptionCreate()) })
pool.Submit(func() error { return image2.Ensure(client.OptionCreate()) })

err := pool.Run(PoolRunArgs{FailFast: false})
```

### PoolRunArgs

```go
type PoolRunArgs struct {
    FailFast bool // Cancel remaining on first error (default: false)
}
```

## Stack

Collects resources and executes actions in priority order:

```go
stack := client.NewStack(project)
stack.Add(profile, image, network, instance)

err := stack.Run(client.ActionEnsure, client.OptionCreate())
```

### Stack Options

```go
stack := client.NewStack(project,
    client.StackWorkers(4),
    client.StackSortDescending(),  // For delete order
)
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

Images in the same batch run in parallel via WorkerPool. All other resources run sequentially.

### Filtering by Action

Get a stack with only resources that support an action:

```go
deleteStack := stack.ForAction(client.ActionDelete)
```

## Helper Functions

### RunAction

Execute an action on a resource:

```go
err := client.RunAction(instance, client.ActionEnsure, client.OptionCreate())
```

### SupportsAction

Check if a resource supports an action:

```go
if client.SupportsAction(resource, client.ActionStart) {
    // resource implements StartAble
}
```

### ByKind

Filter resources by kind with type assertion:

```go
instances, err := client.ByKind[*Instance](resources, client.KindInstance)
```

### FilterDuplicates

Remove duplicate resources from a slice:

```go
unique := client.FilterDuplicates(resources)
```

## Connection Detection

```go
if client.IsRemote() {
    // Connected via HTTPS, bind mounts unavailable
}
```

Used to validate operations like bind mounts that require local access.
