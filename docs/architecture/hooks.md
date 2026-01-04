# Hooks

Hooks intercept resource actions before and after they execute.

## Overview

Every resource action (ensure, delete, start, stop) can be intercepted with hooks:

- **Before hooks** - Run before an action starts
- **After hooks** - Run after an action completes

Hooks receive the action context and can modify errors, abort actions, or add logging.

## Hook Signature

```go
func(action Action, r Resource, args Options, err error) error
```

**Parameters:**

- `action` - The action being performed (ensure, delete, start, stop)
- `r` - The resource being operated on
- `args` - Action options (create, force, timeout)
- `err` - Error from previous hooks or the action

**Return:**

- Modified or original error
- New error to abort the action

## Before Hooks

Before hooks run in FIFO order (first added runs first). Use them for validation, logging, or aborting actions.

### Abort an Action

```go
client.AddHookBefore(func(action Action, r Resource, args Options, err error) error {
    if action == ActionDelete && !confirmed {
        return errors.New("deletion not confirmed")
    }
    return err
})
```

### Log Action Start

```go
logger := slog.Default()

client.AddHookBefore(func(action Action, r Resource, args Options, err error) error {
    logger.Info("Starting action",
        "action", action,
        "resource", r.Name(),
        "kind", r.Kind())
    return err
})
```

### Check for Previous Errors

Before hooks can see errors from earlier before hooks in the chain:

```go
client.AddHookBefore(validateResources)  // Might return error
client.AddHookBefore(func(action Action, r Resource, args Options, err error) error {
    if err != nil {
        // Previous validation failed, skip this hook
        return err
    }
    // Proceed with additional checks
    return nil
})
```

## After Hooks

After hooks run in LIFO order (last added runs first). Use them for logging results, wrapping errors, or cleanup.

### Log Action Results

```go
logger := slog.Default()

client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
    if err != nil {
        logger.Error("Action failed",
            "action", action,
            "resource", r.Name(),
            "kind", r.Kind(),
            "error", err)
        return err
    }

    logger.Info("Action completed",
        "action", action,
        "resource", r.Name(),
        "kind", r.Kind())
    return nil
})
```

### Wrap Errors with Context

```go
client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
    if err != nil {
        return fmt.Errorf("%s %s failed: %w", r.Kind(), r.Name(), err)
    }
    return nil
})
```

### Append Additional Errors

If your hook encounters its own error, append it to the action error:

```go
client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
    if cleanupErr := cleanup(); cleanupErr != nil {
        return errors.Join(err, cleanupErr)
    }
    return err
})
```

## Execution Order

### Before Hooks (FIFO)

Before hooks run first-in-first-out:

```go
client.AddHookBefore(checkPermissions)  // Runs 1st
client.AddHookBefore(validateConfig)    // Runs 2nd
client.AddHookBefore(acquireLock)       // Runs 3rd
```

Order: checkPermissions -> validateConfig -> acquireLock

If any before hook returns an error, the action is aborted.

### After Hooks (LIFO)

After hooks run last-in-first-out:

```go
client.AddHookAfter(logBasicInfo)      // Runs 3rd (inner)
client.AddHookAfter(wrapWithContext)   // Runs 2nd (middle)
client.AddHookAfter(sendToMonitoring)  // Runs 1st (outer)
```

Order: sendToMonitoring -> wrapWithContext -> logBasicInfo

## Common Patterns

### Dry Run Mode

```go
dryRun := true

client.AddHookBefore(func(action Action, r Resource, args Options, err error) error {
    if dryRun && (action == ActionEnsure || action == ActionDelete) {
        log.Printf("Would %s %s %s", action, r.Kind(), r.Name())
        return errors.New("dry run mode")
    }
    return err
})
```

### Progress Tracking

```go
var completed, total int

client.AddHookBefore(func(action Action, r Resource, args Options, err error) error {
    total++
    return err
})

client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
    completed++
    fmt.Printf("Progress: %d/%d\n", completed, total)
    return err
})
```

### Conditional Error Suppression

```go
client.AddHookAfter(func(action Action, r Resource, args Options, err error) error {
    if errors.Is(err, ErrNotFound) && action == ActionDelete {
        // Already deleted, not an error
        return nil
    }
    return err
})
```

## Hook Scope

Hooks registered on GlobalClient are inherited by all projects:

```go
globalClient.AddHookBefore(globalLogger)  // All projects see this
```

Hooks on a project Client only affect that project:

```go
projectA, _ := globalClient.EnsureProject("projectA", true)
projectA.AddHookBefore(projectALogger)  // Only projectA
```

## Best Practices

**Keep hooks simple** - Complex logic should be in separate functions.

**Pass through errors** - If you don't modify the error, return it unchanged:

```go
return err  // Not: return nil
```

**Append, don't replace** - When adding your own error:

```go
return errors.Join(err, myErr)  // Not: return myErr
```

**Check action type** - Not all hooks need to run for all actions:

```go
if action != ActionEnsure {
    return err  // Skip for other actions
}
```
