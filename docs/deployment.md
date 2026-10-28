# Deployment

exemplar runs as one server process with a SQLite database and a blob
directory, both under `data/`. To anonymise real data it calls a Presidio
Analyzer service. The repository ships three ways to run them together:

| File | For |
|------|-----|
| [`Dockerfile`](../Dockerfile) | The exemplar image, on its own |
| [`docker-compose.yml`](../docker-compose.yml) | exemplar and Presidio on one machine |
| [`exemplar.nomad`](../exemplar.nomad) | exemplar and Presidio on a Nomad cluster |

Whatever runs it:

- **One instance per database.** SQLite takes one server. Never run two
  against the same `data/`, and never scale the service up.
- **Plain HTTP.** exemplar has no TLS of its own. Both setups listen on
  this machine (or the cluster network) only; to share it, put a TLS proxy
  (Caddy, Traefik, nginx) in front.
- **Presidio stays private.** Real data goes to it, so it is never
  published: only exemplar reaches it.
- **The anonymisation key** turns on ingest of real data and linked fakes.
  The anonymise tool works without it. Every fake is derived from it, so
  keep it safe and keep it the same: a new key means new fakes. See
  [anonymisation.md](anonymisation.md).
- **`data/` holds real-derived data** in quarantine, and backups of it do
  too. Treat the volume and its backups accordingly.

Configuration is `exemplar-config.toml` in the image, overridden by the
environment variables in [cli.md](cli.md) (`SERVER_PORT`, `PRESIDIO_URL`,
`MAX_INGEST_MB`…). The key comes from `EXEMPLAR_ANON_KEY` or a key file
(`ANON_KEY_FILE`, mode 600).

## Docker image

Each release publishes a signed multi-arch image (`linux/amd64`,
`linux/arm64`) to `ghcr.io/martinsuchenak/exemplar`, tagged with the
version, the minor version (`0.2`) and `latest`:

```bash
docker run -d --name exemplar -p 127.0.0.1:8080:8080 -v exemplar-data:/app/data ghcr.io/martinsuchenak/exemplar:latest
docker exec exemplar exemplar bootstrap   # prints the first admin key, once
```

Or build it from a checkout with `task docker` (the `Dockerfile`; releases
use `Dockerfile.release`, which copies in the release binaries). Verify a
published image with `cosign verify ghcr.io/martinsuchenak/exemplar:<version>
--certificate-identity-regexp 'https://github.com/martinsuchenak/exemplar/'
--certificate-oidc-issuer https://token.actions.githubusercontent.com`.

- The image runs `exemplar serve` as user `app`, UID 10001. A named volume
  just works; a host directory mounted at `/app/data` must be writable by
  that UID (`chown 10001:10001 /srv/exemplar/data`).
- `exemplar` is on the image's `PATH`, so any command runs inside it:
  `docker exec exemplar exemplar backup --output /app/data/backup.tar.gz`.
- A health check polls `/health`.
- Extra profiles or record schemas: mount a directory and point
  `ANON_PROFILES_DIR` or `RECORD_SCHEMAS_DIR` at it.
- `.dockerignore` keeps `tmp/`, `data/`, `.env` and key files out of the
  build, so no local data reaches an image.

## Docker Compose

`docker-compose.yml` runs exemplar and Presidio, with the database in a
named volume.

```bash
cp .env.example .env
# Set EXEMPLAR_ANON_KEY in .env for ingest:  openssl rand -hex 32
docker compose up -d --build
docker compose exec exemplar exemplar bootstrap
export EXEMPLAR_API_KEY=exm_...   # the key bootstrap printed
exemplar datasets list
```

The settings live in `.env`, which git ignores; `.env.example` lists them
with their defaults:

| Setting | Default | Meaning |
|---------|---------|---------|
| `EXEMPLAR_VERSION` | `dev` | Image tag, and the version exemplar reports |
| `EXEMPLAR_BIND` | `127.0.0.1` | Address the API is published on; `0.0.0.0` only behind a TLS proxy |
| `EXEMPLAR_PORT` | `8080` | Port the API is published on |
| `EXEMPLAR_ANON_KEY` | empty | The anonymisation key; empty leaves ingest off |
| `EXEMPLAR_MAX_INGEST_MB` | `200` | Largest upload one ingest or anonymise run accepts |
| `PRESIDIO_IMAGE` | `presidio-analyzer:latest` | Pin a version you have tested |

Presidio takes a minute or two to load its model after it starts; until it
is up, anonymising answers "the anonymiser's detector is unavailable; try
again later" and everything else works.
It needs about 2 GiB of memory.

The key reaches the container as an environment variable, so anyone who can
run `docker inspect` on this machine can read it. On a shared host, use a
key file instead: mount it read-only, owned by UID 10001 with mode 600, and
set `ANON_KEY_FILE` to its path.

**Upgrade:** `git pull && docker compose up -d --build`. Migrations run when
`serve` starts.

**Back up** while it runs, copy the archive out, then remove it from the
volume:

```bash
docker compose exec exemplar exemplar backup --output /app/data/backup.tar.gz
docker compose cp exemplar:/app/data/backup.tar.gz ./backup-$(date +%F).tar.gz
docker compose exec exemplar rm /app/data/backup.tar.gz
```

**Restore** with the server stopped. The archive is streamed in as the
container's user: `docker compose cp` would leave it owned by root, where
`restore` cannot read it.

```bash
docker compose stop exemplar
docker compose run --rm -T --entrypoint sh exemplar -c 'umask 077; cat > /app/data/restore.tar.gz' < backup.tar.gz
docker compose run --rm exemplar restore /app/data/restore.tar.gz --overwrite
docker compose run --rm --entrypoint rm exemplar /app/data/restore.tar.gz
docker compose start exemplar
```

`--overwrite` moves the data it replaces aside in the volume
(`exemplar.db.pre-restore-<time>`, `blobs.pre-restore-<time>`); delete it
with `docker compose exec exemplar rm -r …` once you no longer need it.

See [backup-and-exchange.md](backup-and-exchange.md).

## Nomad

`exemplar.nomad` runs one group: Presidio as a sidecar that starts first,
and exemplar, sharing a bridge network so exemplar reaches Presidio on
`localhost:3000`. exemplar registers the `exemplar` service with Nomad's
service discovery, with a `/health` check.

The cluster needs:

- clients with the Docker driver and the CNI plugins (bridge networking);
- the image where clients can pull it: the job uses the published
  `ghcr.io/martinsuchenak/exemplar:latest` unless `-var image=…` names
  another (pin a version in production);
- a host volume called `exemplar-data`, writable by UID 10001, in the
  client's configuration. It pins the job to that client:

  ```hcl
  client {
    host_volume "exemplar-data" {
      path      = "/opt/exemplar/data"
      read_only = false
    }
  }
  ```

Then:

```bash
# The anonymisation key (optional: without it, ingest stays off)
nomad var put nomad/jobs/exemplar anon_key=$(openssl rand -hex 32)

nomad job run -var image=ghcr.io/martinsuchenak/exemplar:0.1.0 exemplar.nomad

# The first admin key
nomad alloc exec -task exemplar <alloc-id> exemplar bootstrap
```

The job's variables are `image`, `presidio_image` (pin a tested version)
and `datacenters`. The key is read from the Nomad variable
`nomad/jobs/exemplar` (item `anon_key`) into the task's environment; only
this job and holders of a token that can read that variable see it.
Changing the variable restarts the task.

`count` is 1 and must stay 1 (SQLite). An update stops the old allocation
before the new one starts, so two servers never share the database. The
task gets 35 seconds to stop, enough for requests in flight to finish.
Back up with `nomad alloc exec -task exemplar <alloc-id> exemplar backup
--output /app/data/backup.tar.gz`; the archive lands in the host volume.
