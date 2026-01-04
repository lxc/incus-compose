# Image Package

The Image resource handles OCI image pulling and caching in Incus.

## ImageConfig

Configuration for image sources:

```go
type ImageConfig struct {
    // Source is the image server to copy the image from.
    Source incusClient.ImageServer

    // Cache is the instance server where images are cached.
    // Defaults to client.imageCache if not specified.
    Cache incusClient.InstanceServer

    // Remote is the domain part of the image reference.
    Remote string

    // Image is the image reference without the remote prefix.
    Image string
}
```

## Image

Represents a cached OCI image:

```go
type Image struct {
    *BaseResource

    client    *Client
    Config    ImageConfig
    incusName string

    // State - nil means not ensured
    IncusAlias *incusApi.ImageAliasesEntry
    ETag       string
}
```

## Image Flow

### Reference Parsing

Docker-style references are parsed using `github.com/distribution/reference`:

```go
// Input: "docker.io/nginx:alpine"
// Parsed:
//   Remote: "docker.io"
//   Image:  "library/nginx:alpine"

// Input: "ghcr.io/myorg/myapp:v1.0"
// Parsed:
//   Remote: "ghcr.io"
//   Image:  "myorg/myapp:v1.0"
```

### Cache Lookup

Before downloading, check if image alias exists:

```go
alias, eTag, err := config.Cache.GetImageAlias(incusName)
if err == nil {
    // Already cached, use existing
    return alias, nil
}
```

### Copy Operation

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

### Progress Reporting

Copy operations report progress via hookRemoteOperation:

```go
c.hookRemoteOperation = func(action, r, args, op, err) error {
    op.AddHandler(func(opAPI incusApi.Operation) {
        // Extract progress from metadata
        for key, val := range opAPI.Metadata {
            if strings.HasSuffix(key, "_progress") {
                // Parse "50% (1.2MB/s)" format
            }
        }
    })
    return op.Wait()
}
```

## Global Cache

Images are cached globally (not per-project) for Docker compatibility:

```go
// GlobalClient level - shared across all projects
type GlobalClient struct {
    imageCache incusClient.InstanceServer
}

// Client inherits global cache
type Client struct {
    imageCache incusClient.InstanceServer  // same as globalClient.imageCache
}
```

This ensures:

- Images are downloaded once
- Multiple projects share the same cache
- Matches Docker behavior

## Parallel Downloads

Images have their own priority level (1024) so they batch together.

When Stack.Run sees an image batch with 2+ images, it uses WorkerPool:

```go
if kind == KindImage && len(batch) > 1 {
    pool := NewWorkerPool(s.workers)
    for _, r := range batch {
        pool.Submit(func() error {
            return RunAction(r, action, opts...)
        })
    }
    pool.Run(PoolRunArgs{FailFast: false})
}
```

## Auto-Update

Images are configured with `AutoUpdate: true`. Incus periodically checks the source registry and refreshes the cached image. Running containers are not affected; new containers use the updated image.

## Delete

Removing an image by fingerprint:

```go
func (r *Image) Delete(opts ...Option) error {
    op, err := r.Config.Cache.DeleteImage(r.IncusAlias.Target)
    // Wait via hookOperation
}
```

Note: `incus-compose down` does not delete images. Use `incus image delete` manually.

## Source Configuration

The Source field requires an ImageServer. This must be configured before calling Ensure:

```go
// Load from Incus CLI config
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

## Podman Compatibility

Images with "localhost" remote (common in podman) are converted to "local":

```go
if config.Remote == "localhost" {
    config.Remote = "local"
}
```
