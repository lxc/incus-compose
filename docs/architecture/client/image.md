# Image Resource

The Image resource handles OCI image pulling and caching in Incus.

## ImageConfig

Configuration for image sources:

```go
type ImageConfig struct {
    // Source is the image server to copy the image from.
    Source incusClient.ImageServer

    // Cache is the instance server where images are cached.
    // Defaults to Client.imageCache (global cache) if not specified.
    Cache incusClient.InstanceServer

    // Remote is the domain part of the image reference.
    Remote string

    // Image is the image reference without the remote prefix.
    Image string
}
```

### Cache vs Source

- **Source**: Where to download images from (e.g., docker.io registry)
- **Cache**: Where to store downloaded images

The default cache is the global image cache (default project). For project-isolated caching, use `Client.Connection()`:

```go
// Global cache (default) - images shared across projects
img, _ := project.Resource(client.KindImage, "docker.io/nginx:alpine", &client.ImageConfig{
    Source: imageServer,
    // Cache defaults to global cache
})

// Project-scoped cache - images isolated to this project
img, _ := project.Resource(client.KindImage, "docker.io/nginx:alpine", &client.ImageConfig{
    Source: imageServer,
    Cache:  project.Connection(),
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

Before downloading, check if image alias exists:

```go
alias, eTag, err := config.Cache.GetImageAlias(incusName)
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

### 3. Copy Operation

Images are copied from source to cache:

```go
imgInfo := &incusApi.Image{
    Fingerprint: config.Image,
}
imgInfo.Public = true

copyArgs := &incusClient.ImageCopyArgs{
    Aliases:    []incusApi.ImageAlias{{Name: incusName}},
    AutoUpdate: true,
    Public:     false,
    Mode:       "pull",
}

op, err := config.Cache.CopyImage(config.Source, *imgInfo, copyArgs)
```

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

Removing an image by fingerprint:

```go
err := client.RunAction(img, client.ActionDelete, client.OptionForce())
```

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
