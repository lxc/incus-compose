# Builds

incus-compose supports building local service images from Compose `build:` definitions and importing the result into the Incus project.

> Build support requires `podman` or `docker` on the machine running incus-compose.

incus-compose does not implement a builder itself and does not use the Buildah Go library. It shells out to a local container builder, then imports the built rootfs into Incus as an image.

Builder selection:

1. `INCUS_COMPOSE_BUILDER`, when set
2. `podman`, when found in `PATH`
3. `docker`, when found in `PATH`

Examples:

```bash
INCUS_COMPOSE_BUILDER=podman incus-compose build
INCUS_COMPOSE_BUILDER=docker incus-compose up --build
```

If no builder is found, build-configured services fail with an error.

## Basic usage

Build all services that define `build:`:

```bash
incus-compose build
```

Build selected services:

```bash
incus-compose build web worker
```

Start services, building missing build-configured images as needed:

```bash
incus-compose up
```

Force rebuild before starting:

```bash
incus-compose up --build
```

Require built images to already exist:

```bash
incus-compose up --no-build
```

## Compose examples

Short syntax:

```yaml
services:
  web:
    build: .
```

Object syntax with an explicit image name:

```yaml
services:
  web:
    image: localhost/web:latest
    build:
      context: .
      dockerfile: Containerfile
```

When `image:` is omitted, incus-compose uses a local image name based on the project and service:

```text
localhost/<project>-<service>
```

## Supported build options

| Option              | Support                                                                                             |
| ------------------- | --------------------------------------------------------------------------------------------------- |
| `context`           | Build context directory. Relative paths are resolved by compose-go.                                 |
| `dockerfile`        | Alternate Dockerfile or Containerfile path.                                                         |
| `dockerfile_inline` | Inline Dockerfile content. incus-compose writes it to a temporary file before invoking the builder. |
| `args`              | Build arguments, passed as `--build-arg KEY=VALUE`. Args without values are ignored.                |
| `no_cache`          | Passed as `--no-cache`.                                                                             |
| `pull`              | Passed as `--pull`.                                                                                 |
| `target`            | Multi-stage build target, passed as `--target`.                                                     |
| `platforms`         | A single platform is supported. Multiple platforms are rejected.                                    |
| service `platform`  | Used as the build platform when `build.platforms` is not set.                                       |

## Platform handling

Built images must match an architecture supported by the target Incus server.

incus-compose asks Incus for supported server architectures and uses the first architecture as the default build target. For example:

```yaml
environment:
  architectures:
    - x86_64
    - i686
```

This builds with:

```text
--platform linux/amd64
```

and imports the image with Incus metadata architecture:

```text
x86_64
```

Supported architecture mappings include:

| Incus architecture | Builder platform |
| ------------------ | ---------------- |
| `x86_64`           | `linux/amd64`    |
| `i686`             | `linux/386`      |
| `aarch64`          | `linux/arm64`    |
| `armv7`, `armv7l`  | `linux/arm/v7`   |
| `armv6`, `armv6l`  | `linux/arm/v6`   |
| `ppc64le`          | `linux/ppc64le`  |
| `s390x`            | `linux/s390x`    |
| `riscv64`          | `linux/riscv64`  |

If a service requests a platform that Incus does not report as supported, the build fails before invoking the builder.

## Build command options

```bash
incus-compose build [SERVICE...]
```

| Option       | Description                                                                            |
| ------------ | -------------------------------------------------------------------------------------- |
| `--no-cache` | Disable builder cache for this build. Also enabled when `build.no_cache: true` is set. |
| `--pull`     | Pull newer base images for this build. Also enabled when `build.pull: true` is set.    |

## up build behavior

For build-configured services, `up` defaults to building only when the Incus image is missing.

| Command                       | Behavior                                                                       |
| ----------------------------- | ------------------------------------------------------------------------------ |
| `incus-compose up`            | Build missing build-configured images. Use existing built images when present. |
| `incus-compose up --build`    | Force rebuild build-configured images.                                         |
| `incus-compose up --no-build` | Never build. Fail if a required built image is missing.                        |

## Unsupported build options

The following Compose build options are currently not implemented:

- `additional_contexts`
- `cache_from`
- `cache_to`
- `entitlements`
- `extra_hosts`
- `isolation`
- `labels`
- `network`
- `privileged`
- `provenance`
- `sbom`
- `secrets`
- `shm_size`
- `ssh`
- `tags`
- `ulimits`

`tags` are intentionally ignored for now. incus-compose imports the built artifact into Incus and uses the Incus image alias needed by the project; extra Docker-style tags do not affect runtime behavior.

## See Also

- [CLI Reference](cli.md#build) - `build` command flags and `up` build behavior
- [Compose Compatibility](compose-compatibility.md) - overall feature support
- [Getting Started](getting-started.md) - first project walkthrough
