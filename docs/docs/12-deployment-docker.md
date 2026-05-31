# 12. Deployment with Docker

VeriProc ships two Docker images, both built from the repository root. This page covers
building, configuring, and running them with Docker Compose.

## 12.1 Images

| Image | Contents | Default port |
|-------|----------|--------------|
| `veriproc` (daemon) | `veriprocd` daemon + `veriproc` CLI | 8080 |
| `veriproc-console` | `veriproc-console` gateway + pre-built webapp | 8090 |

Each image is tagged with the version string (git tag or `MAKEFILE_VERSION`) and `latest`.
The default registry prefix is `ghcr.io/leonid-butenko` (override with `DOCKER_REGISTRY`).

## 12.2 Build

```bash
# Build both images (picks up VERSION / GIT_COMMIT / BUILD_DATE automatically)
make docker-build

# Build a single image
make docker-build-daemon
make docker-build-console

# Override the registry
make docker-build DOCKER_REGISTRY=registry.example.com/veriproc

# Tag-driven release
git tag v0.6.0
make docker-build            # produces veriproc:v0.6.0 and veriproc-console:v0.6.0
make docker-push             # pushes :v0.6.0 and :latest
```

The Dockerfiles live in `docker/` and use multi-stage builds (Go builder
`golang:1.25-alpine`, runtime `alpine:3.21`). Pin these to digests for reproducible
production builds.

## 12.3 Compose stack

An example [`docker/docker-compose.yaml`](../../docker/docker-compose.yaml) runs the daemon
and console together with health-gated startup.

```bash
# 1. Build images
make docker-build

# 2. Prepare config
mkdir -p deploy/stations
cp sandbox/instance.yaml        deploy/instance.yaml
cp sandbox-console/console.yaml deploy/console.yaml
# Edit deploy/*.yaml (see adjustments below)

# 3. Start
cd docker
docker compose up -d

# 4. Verify
docker compose ps
docker compose logs -f
```

The console UI is then available at `http://localhost:8090/`.

### Config adjustments for containers

When adapting the sandbox configs for Docker, set paths to the mounted volumes:

- `db.dsn` → a path inside `/data`, e.g. `sqlite:///data/veriproc.db`
- `storage.working_root_base` → `/data/working-roots`
- `storage.station_config_root` → `/stations`
- In `console.yaml`: `db.dsn` → `file:/data/console.db`
- `instances[].base_url` → `http://veriprocd:8080` (the compose service name)
- Replace all sandbox tokens with secure values

## 12.4 Volume mount points

**`veriprocd`**

| Mount | Purpose |
|-------|---------|
| `/config/instance.yaml` | Instance configuration (required). |
| `/stations` | Station YAML directory (read-only). |
| `/data` | SQLite database and working roots (read-write). |
| `/archives` | Rolling-archive roots (read-only, optional). |

**`veriproc-console`**

| Mount | Purpose |
|-------|---------|
| `/config/console.yaml` | Console configuration (required). |
| `/data` | Console SQLite database (audit, UI state) (read-write). |

The console image's entrypoint already points `--webapp-dir` at the bundled
`/srv/webapp/dist`.

## 12.5 Health checks

The daemon container exposes `GET /health`; the example compose uses it as a `healthcheck`
and gates the console's startup on `service_healthy`. Wire `/health` (liveness) and
`/readiness` (readiness) to your orchestrator's probes — see
[Operations §11.8](11-operations.md).

## 12.6 Production hardening checklist

- Replace all sandbox tokens; treat config files as secrets.
- Enable authentication on the daemon and give the console an appropriate upstream token.
- Mount rolling archives read-only unless the instance must publish into them.
- Keep `allowed_roots` in the console config as narrow as possible.
- Pin base-image digests in the Dockerfiles.
- Ensure the `/data` volume is backed up — it holds the authoritative control state and
  the working roots.
- For SLURM executors, ensure the shared filesystem is mounted consistently in the daemon
  container and on the compute nodes.
