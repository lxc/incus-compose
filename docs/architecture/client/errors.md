# Errors

The client package uses a sentinel-based error system with automatic context enrichment.

## Automatic Resource Context

All errors returned from resource operations are automatically enriched with resource context via the `hookAfter` mechanism:

```go
// This happens automatically for every resource operation
err := client.RunAction(instance, client.ActionEnsure, client.OptionCreate())
// If err != nil, it includes: "... : Instance(myapp-web)"
```

**How it works:**

1. Any error returned from a resource operation passes through `hookAfter`
2. If the error is already a `*client.Error`, it gets `.WithResource(r)` added
3. If the error is any other type, it gets wrapped: `ErrUnknown.WithResource(r).Wrap(err)`

This means you never need to manually add resource context to errors - the client does it automatically.

## Sentinel Errors

Errors are sentinel-based, allowing reliable error checking with `errors.Is()`:

```go
err := client.RunAction(instance, client.ActionEnsure)
if errors.Is(err, client.ErrNotFound) {
    // Handle not found
}
if errors.Is(err, client.ErrOperation) {
    // Handle operation failure
}
```

### Available Sentinels

| Error | Description |
|-------|-------------|
| `ErrNotFound` | Resource was not found |
| `ErrNotEnsured` | Operation requires resource to be ensured first |
| `ErrOperation` | Error within an Incus operation |
| `ErrCreate` | Resource creation failed |
| `ErrAborted` | Operation aborted (e.g., by hook) |
| `ErrConnectionFailed` | Connection attempt failed |
| `ErrDisconnected` | Client is not connected |
| `ErrUnsupportedAction` | Resource does not support the action |
| `ErrUnknownResource` | Unknown resource kind |
| `ErrUnknownConfig` | Unknown config for resource |
| `ErrInvalidFormat` | Invalid format or syntax |
| `ErrImageSource` | Image source error |
| `ErrImageRequired` | Instance requires an image |
| `ErrDeviceConflict` | Device name conflict |
| `ErrVolumeMismatch` | Volume configuration mismatch |
| `ErrBadDeviceConfig` | Bad device configuration |
| `ErrBindMountRemote` | Bind mounts not supported over network |
| `ErrDependencyNotEnsured` | Dependency not ensured |
| `ErrNilPointer` | Nil pointer encountered |
| `ErrUnknown` | Unknown error (wraps non-Error types) |

## Error Methods

The `*Error` type supports the standard Go error interface methods:

```go
// errors.Is() - check sentinel identity
if errors.Is(err, client.ErrNotFound) { ... }

// errors.As() - extract the *Error with full context
var clientErr *client.Error
if errors.As(err, &clientErr) {
    fmt.Println(clientErr.Error()) // Full message with context
}

// errors.Unwrap() - get the wrapped underlying error
underlying := errors.Unwrap(err)
```

## Creating Contextual Errors

Within the client package, errors are created with context using method chaining:

```go
// Add resource context (usually automatic via hookAfter)
return ErrNotFound.WithResource(r)

// Add kind and name
return ErrNotFound.WithKindName(KindInstance, "web")

// Add action context
return ErrOperation.WithAction(ActionEnsure)

// Add text context
return ErrInvalidFormat.WithText("expected host:port")

// Wrap another error
return ErrOperation.Wrap(incusErr)

// Combine them
return ErrCreate.WithResource(r).Wrap(incusErr)
```

Each `With*` method returns a new `*Error` preserving the sentinel identity, so `errors.Is()` continues to work correctly.

`Wrap(nil)` returns a non-nil `*Error` (a sentinel with no cause), so only wrap a
real error: wrapping unconditionally turns a success path into a spurious failure
(common in after hooks, see [Hooks](hooks.md#attribute-failures-in-complex-hooks)).
