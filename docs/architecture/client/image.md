# Image Resource

The Image resource handles OCI image pulling and caching in Incus.

## 3-Stage Image Flow

Images go through three stages:

1. **Remote** - OCI registry (docker.io, ghcr.io)
2. **Cache** - Local image store (`incus-compose-images` project)
3. **Project** - Project-scoped copy for instance use

This design provides:

- **Faster subsequent runs** - no re-pulling from registry
- **No registry rate limits** - cached locally after first pull
- **Project isolation** - each project gets its own copy
- **Safe cleanup** - deleting project images doesn't affect cache

## Image Status

Images report their status via `Status()`:

| Status  | Description                            |
| ------- | -------------------------------------- |
| Unknown | Not downloaded yet                     |
| Cached  | In cache project, ready to copy        |
| Exists  | Copied to project, ready for instances |

## ImageConfig

Configuration for image sources:

```go
type ImageConfig struct {
    // Source is the image server to copy the image from.
    Source incusClient.ImageServer

    // CacheServer is an image server to use as cache (for library users).
    // Takes precedence over CacheProject.
    CacheServer incusClient.InstanceServer

    // CacheProject is the project name to use as cache (for CLI users).
    // The project will be created if it doesn't exist.
    // Ignored if CacheServer is set.
    CacheProject string

    // Remote is the domain part of the image reference.
    Remote string

    // Image is the image reference without the remote prefix.
    Image string
}
```

### Cache Configuration

- **CacheServer**: For library users who manage their own cache
- **CacheProject**: For CLI users, specifies project name (auto-created)
- **Default**: Uses `incus-compose-images` project

```go
// Library usage - provide your own cache server
img, _ := project.Resource(client.KindImage, "docker.io/nginx:alpine", &client.ImageConfig{
    Source:      imageServer,
    CacheServer: myCacheServer,
})

// CLI usage - specify cache project name
img, _ := project.Resource(client.KindImage, "docker.io/nginx:alpine", &client.ImageConfig{
    Source:       imageServer,
    CacheProject: "my-image-cache",
})
```

## Image Reference Parsing

Docker-style references are parsed using `github.com/distribution/reference`:

```go
// Input: "nginx:alpine"
// Parsed:
//   Remote: "docker.io"
//   Image:  "library/nginx:alpine"

// Input: "docker.io/library/alpine:3.18"
// Parsed:
//   Remote: "docker.io"
//   Image:  "library/alpine:3.18"

// Input: "ghcr.io/myorg/myapp:v1.0"
// Parsed:
//   Remote: "ghcr.io"
//   Image:  "myorg/myapp:v1.0"

// Input: "alpine" (no tag)
// Parsed:
//   Remote: "docker.io"
//   Image:  "library/alpine:latest"
```

Config can override parsing:

```go
img, _ := project.Resource(client.KindImage, "custom-name", &client.ImageConfig{
    Source: imageServer,
    Remote: "custom.registry.io",
    Image:  "myimage:v2",
})
```

## Ensure Flow

### 1. Check Cache

Before downloading, check if image alias exists in cache:

```go
alias, eTag, err := config.cache.GetImageAlias(incusName)
if err == nil {
    // Already cached
    return nil
}
```

### 2. Create Option Required

If image not found and `OptionCreate()` not set, returns `ErrNotFound`:

```go
// Fails if image not cached
err := client.RunAction(img, client.ActionEnsure)

// Downloads if not cached
err := client.RunAction(img, client.ActionEnsure, client.OptionCreate())
```

### 3. Copy to Cache

Images are copied from source (registry) to cache:

```go
copyArgs := &incusClient.ImageCopyArgs{
    Aliases:    []incusApi.ImageAlias{{Name: incusName}},
    AutoUpdate: true,
    Public:     false,
    Mode:       "pull",
}

op, err := config.cache.CopyImage(config.Source, *imgInfo, copyArgs)
```

### 4. Copy to Project

Before creating an instance, images are copied from cache to project:

```go
err := image.CopyTo(projectClient)
```

This is called automatically by `Instance.Ensure()`. The copy is local and fast.

## Source Configuration

The Source field requires an ImageServer from Incus CLI config:

```go
conf, _ := cliconfig.LoadConfig("")
imageServer, _ := conf.GetImageServer("docker.io")

img, _ := project.Resource(client.KindImage, "docker.io/nginx:alpine", &client.ImageConfig{
    Source: imageServer,
})
```

Registries must be configured as Incus remotes:

```bash
incus remote add --protocol oci docker.io https://docker.io
incus remote add --protocol oci ghcr.io https://ghcr.io
```

Calling Ensure with `OptionCreate()` but no Source returns an error:

```go
img, _ := project.Resource(client.KindImage, "docker.io/nginx:alpine", &client.ImageConfig{
    // No Source!
})
err := client.RunAction(img, client.ActionEnsure, client.OptionCreate())
// err: "image source not configured"
```

## Delete

Delete only removes the image from the project, not from the cache:

```go
err := client.RunAction(img, client.ActionDelete, client.OptionForce())
```

This preserves the cached image for future use. The cache persists across test runs.

Note: `incus-compose down` does not delete images by default.

## Podman Compatibility

Images with "localhost" remote (common in podman) are converted to "local":

```go
// Input: "localhost/myimage:latest"
// Remote becomes: "local"
```

## Priority and Parallel Downloads

Images have priority 1024, placing them after profiles but before networks.

When Stack.Run processes multiple images, they download in parallel via WorkerPool.

## Auto-Update

Images are configured with `AutoUpdate: true`. Incus periodically checks the source registry and refreshes the cached image. Running containers are not affected; new containers use the updated image.
