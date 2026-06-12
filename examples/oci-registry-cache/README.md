# OCI Registry Cache for Incus

This fixture runs three [distribution](https://github.com/distribution/distribution) registry instances as pull-through caches, one per upstream registry. Incus remotes are then reconfigured to point at these local caches instead of the real upstream endpoints, so container images are fetched once and served locally on subsequent pulls.

| Service           | Upstream                       | Static IP           |
| ----------------- | ------------------------------ | ------------------- |
| `docker-registry` | `https://registry-1.docker.io` | `10.132.32.17:5000` |
| `ghcr-registry`   | `https://ghcr.io`              | `10.132.32.18:5000` |
| `gitlab-registry` | `https://registry.gitlab.com`  | `10.132.32.19:5000` |

Each cache holds images for 168 h (7 days) before re-validating with the upstream.

## Prerequisites

The registry image itself lives on `docker.io`, which creates a bootstrapping problem: you can't pull the registry image through the proxy before the proxy exists. The solution is to add a direct (non-proxied) remote for the initial pull:

```sh
incus remote add --protocol oci direct-docker.io https://docker.io
```

This remote is used by `compose.yaml` (`image: direct-docker.io/library/registry:3`) and can be left in place permanently — it is only contacted during `incus-compose up` to pull or update the registry image.

## Setup

### 1. Expose the caches via a reverse proxy

The registry instances listen on their static IPs inside the Incus network. A TLS-terminating reverse proxy is required to expose them as proper HTTPS endpoints (Incus remotes require HTTPS).

**Caddy example** — the IPs must match those in `compose.incus.yaml`:

```Caddyfile
docker-registry.example.com {
	log {
		output file /var/log/caddy/docker-registry.example.com-access.log
	}

	reverse_proxy 10.132.32.17:5000
}

ghcr-registry.example.com {
	log {
		output file /var/log/caddy/ghcr-registry.example.com-access.log
	}

	reverse_proxy 10.132.32.18:5000
}

gitlab-registry.example.com {
	log {
		output file /var/log/caddy/gitlab-registry.example.com-access.log
	}

	reverse_proxy 10.132.32.19:5000
}
```

### 2. Start the caches

```sh
cd registry
incus-compose up
```

### 3. Point Incus remotes at the local caches

Replace the Incus remotes with your new endpoints. Any subsequent `incus image copy` or container launch will hit the local cache first.

```sh
incus remote remove docker.io
incus remote add --protocol oci docker.io https://docker-registry.example.com

incus remote remove ghcr.io
incus remote add --protocol oci ghcr.io https://ghcr-registry.example.com

incus remote remove registry.gitlab.com
incus remote add --protocol oci registry.gitlab.com https://gitlab-registry.example.com
```

## Notes

- The startup command in `compose.incus.yaml` includes a `sleep 10s` delay. This is a workaround for a race condition where `registry serve` starts before the Incus network interface is fully ready.
- Cache storage is backed by named Incus volumes (`docker_registry_cache`, `ghcr_registry_cache`, `gitlab_registry_cache`) and survives container restarts.
- To add another registry (e.g. `quay.io`), add a new service to both compose files with the next available static IP and a matching Caddy block.

## Reference

- [distribution on Docker Hub](https://hub.docker.com/_/registry)
- [Configuration reference](https://distribution.github.io/distribution/about/configuration/)
- [Deployment guide](https://distribution.github.io/distribution/about/deploying/)
