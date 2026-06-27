# Setting up a self-hosted GitHub Actions runner

This guide brings up a self-hosted GitHub Actions runner for `lxc/incus-compose`
inside a privileged Incus container that runs its own (nested) Incus daemon, so
the test suite can create instances, networks and volumes.

The steps move between three shells. Each section says which one it runs in:

- **Host** — your workstation/server running Incus.
- **Container (root)** — a root shell inside the `runner-local` container.
- **Runner user** — an unprivileged `runner` login shell inside the container.

Placeholders to replace as you go: `example.com` (your OCI registry mirror
domain), `<ip-from-above>` (the container's bridge IP), and the `--token`
registration token from GitHub.

## 1. Create the runner container — _host_

```bash
incus --project=ic-github-runner launch images:debian/trixie runner-local -c security.privileged=true -c security.nesting=true
incus --project=ic-github-runner exec runner-local /bin/bash
```

The `exec` drops you into a root shell inside the container; the next steps run
there.

## 2. Install base packages — _container (root)_

```bash
apt install sudo sudo-rs vim golang git shellcheck
ln -s /usr/sbin/sudo-rs /usr/local/sbin/sudo
```

## 3. Install Incus from the Zabbly repository — _container (root)_

```bash
curl -fsSL https://pkgs.zabbly.com/key.asc -o /etc/apt/keyrings/zabbly.asc
sh -c 'cat <<EOF > /etc/apt/sources.list.d/zabbly-incus-stable.sources
Enabled: yes
Types: deb
URIs: https://pkgs.zabbly.com/incus/stable
Suites: $(. /etc/os-release && echo ${VERSION_CODENAME})
Components: main
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/zabbly.asc

EOF'

apt update; apt install incus podman skopeo xdelta3 umoci
```

## 4. Create the `runner` user — _container (root)_

The `incus-admin` group lets the runner talk to the nested Incus daemon without
`sudo`.

```bash
adduser --disabled-password --shell /usr/bin/bash runner
usermod -aG incus-admin runner
```

## 5. Install golangci-lint — _runner user_

The install script drops the binary in `~/.local/bin`. Create that directory _before_
logging in: Debian's `~/.profile` only adds `~/.local/bin` to `PATH` if it exists at
login, so log out and back in afterwards to pick it up.

```bash
sudo -u runner -iH
mkdir -p ~/.local/bin
curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b ~/.local/bin
exit

sudo -u runner -iH
which golangci-lint
```

## 6. Initialise the nested Incus daemon — _runner user_

Accept the defaults unless you have a reason not to.

```bash
incus admin init
```

## 7. Add OCI registry remotes — _runner user_

These point at your registry mirrors so images can be pulled by short name.

```bash
export DOMAIN=example.com
incus remote add --protocol=oci docker.io https://docker-registry.$DOMAIN
incus remote add --protocol=oci ghcr.io https://ghcr-registry.$DOMAIN
incus remote add --protocol=oci registry.gitlab.com https://gitlab-registry.$DOMAIN
```

## 8. Enable HTTPS access to the local daemon — _runner user_

Generate a client certificate, trust it, find the bridge IP, expose the daemon
over HTTPS, and add a remote pointing at it.

```bash
incus remote generate-certificate
incus config trust add-certificate ~/.config/incus/client.crt
ip a show dev incusbr0
export IP=<ip-from-above>
incus config set core.https_address=:8443
incus remote add local-https $IP --accept-certificate
```

## 9. Download the GitHub Actions runner — _runner user_

```bash
mkdir actions-runner; cd actions-runner
curl -o actions-runner.tar.gz -L https://github.com/actions/runner/releases/download/v2.335.1/actions-runner-linux-x64-2.335.1.tar.gz
tar xf actions-runner.tar.gz; rm -f actions-runner.tar.gz
exit
```

## 10. Install runner dependencies — _container (root)_

The dependency installer needs root, so run it after the `exit` above.

```bash
/home/runner/actions-runner/bin/installdependencies.sh
```

## 11. Register the runner — _runner user_

Get a registration token from the repository's **Settings → Actions → Runners →
New self-hosted runner**, then register:

```bash
sudo -u runner -iH
cd actions-runner
./config.sh --url https://github.com/lxc/incus-compose --token XXX
```

The interactive prompts look like this (the values shown are the ones used
here):

```
--------------------------------------------------------------------------------
|        ____ _ _   _   _       _          _        _   _                      |
|       / ___(_) |_| | | |_   _| |__      / \   ___| |_(_) ___  _ __  ___      |
|      | |  _| | __| |_| | | | | '_ \    / _ \ / __| __| |/ _ \| '_ \/ __|     |
|      | |_| | | |_|  _  | |_| | |_) |  / ___ \ (__| |_| | (_) | | | \__ \     |
|       \____|_|\__|_| |_|\__,_|_.__/  /_/   \_\___|\__|_|\___/|_| |_|___/     |
|                                                                              |
|                       Self-hosted runner registration                        |
|                                                                              |
--------------------------------------------------------------------------------

# Authentication


√ Connected to GitHub

# Runner Registration

Enter the name of the runner group to add this runner to: [press Enter for Default]

Enter the name of runner: [press Enter for runner-local] server01-runner-local

This runner will have the following labels: 'self-hosted', 'Linux', 'X64'
Enter any additional labels (ex. label-1,label-2): [press Enter to skip] incus-compose-local

√ Runner successfully added

# Runner settings

Enter name of work folder: [press Enter for _work]

√ Settings Saved.
```

## 12. Run the runner as a service — _container (root)_

```bash
exit
pushd /home/runner/actions-runner
./svc.sh install runner
./svc.sh start
```

The runner is now registered and starts automatically with the container.
