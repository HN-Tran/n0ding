# Deployment

n0ding ships as a multi-architecture Linux container for AMD64 and ARM64. The
same image runs through Docker Desktop on Windows and macOS. The default
deployment binds only to loopback and is intended for private-alpha use.

## Install a specific version

Linux and macOS:

```sh
N0DING_VERSION=v0.1.0 sh install.sh
```

Windows PowerShell:

```powershell
.\install.ps1 -Version v0.1.0
```

The default install directory is `~/.n0ding`. Override it with
`N0DING_INSTALL_DIR` or `-InstallDir` on PowerShell. The generated `.env` pins
the container image to the selected release. Deployment assets are verified
against the release's `SHA256SUMS` file before the container starts.

## Operate the service

Run these commands from the install directory, or pass it with
`--project-directory`:

```sh
docker compose ps
docker compose logs -f
docker compose restart
docker compose stop
docker compose start
```

The named `n0ding-data` volume survives `docker compose down` and upgrades.

## Upgrade or roll back

Run the installer again with the desired version. It replaces only the
deployment files and `.env`, then pulls and starts that version. Cached data
remains in the named volume.

Before upgrades, follow the stopped-volume procedure in the
[backup and restore drill](backup-restore-drill.md).

## Shared deployment

The generated `.env` contains:

```dotenv
N0DING_BIND_ADDRESS=127.0.0.1
N0DING_PORT=8080
N0DING_PUBLIC_URL=http://localhost:8080
```

Keep the loopback binding and place Caddy, nginx, or another TLS reverse proxy
on the same host in front of n0ding. Set `N0DING_PUBLIC_URL` to the exact HTTPS
URL clients use. Review the [operations guide](operations.md) before exposing
the service to a network.

n0ding currently has no client authentication or RBAC. Do not expose it
directly to the public internet.

## Uninstall

Stop and remove the service while preserving cached data:

```sh
docker compose down
```

After that, remove the install directory manually. To also delete cached data,
run the following only when data loss is intentional:

```sh
docker compose down --volumes
```

## Release integrity

Release images include BuildKit provenance and an SBOM in GHCR. GitHub also
publishes a build-provenance attestation for the image digest. Deployment
files and installers are listed in `SHA256SUMS` on every release.
