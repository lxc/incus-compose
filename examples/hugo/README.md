# hugo

Example of a `build:` service: Hugo generates a static site, nginx-unprivileged serves it.
The build is defined inline via `dockerfile_inline` — no separate Dockerfile needed.
`compose.incus.yaml` passes the static IP as `BASE_URL` so Hugo generates correct links.

## Usage

```bash
incus-compose up
```

Open http://10.134.32.17:8080/

## How it works

`compose.yaml` defines the build with `dockerfile_inline`:

1. `alpine` installs Hugo and builds `site/` into `public/`
2. `nginxinc/nginx-unprivileged:alpine` serves the output

`compose.incus.yaml` overrides `BASE_URL` with the static IP so Hugo's generated
links point to the right place.
