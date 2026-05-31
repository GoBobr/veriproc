# 4. Installation & Build

VeriProc builds from source with the Go toolchain. The operator web console additionally
requires Node.js to build the frontend bundle.

## 4.1 Prerequisites

- **Go toolchain** (module targets Go 1.25). Required for all three binaries.
- **Node.js 18+ and npm** — only needed if you want to build the web console frontend.
- **Python 3** — optional, only if you want a user-local Node.js via `nodeenv`.

The SQLite driver is pure Go (`modernc.org/sqlite`), so **no C compiler or system SQLite is
required**.

## 4.2 Build the binaries

From the repository root:

```bash
make build
```

This produces:

- `bin/veriprocd` — the processing daemon
- `bin/veriproc` — the CLI
- `bin/veriproc-console` — the console gateway

Each binary is stamped with version metadata via linker flags (`internal/version`): the
git tag if the current commit is tagged, otherwise the `MAKEFILE_VERSION` baseline
(currently `0.5-dev`), plus the short commit and build date. Verify with:

```bash
./bin/veriproc version
```

### Useful Make targets

| Target | Effect |
|--------|--------|
| `make build` | Build all three binaries into `bin/` |
| `make test` | Run the full test suite with the race detector |
| `make test-short` | Run the short test suite |
| `make run` | `go run` the daemon directly |
| `make fmt` / `make vet` / `make tidy` | Formatting, vetting, module tidy |
| `make webapp-install` | `npm install` for the webapp |
| `make webapp-build` | Build the production frontend bundle into `webapp/dist` |
| `make webapp-test` | Run frontend tests |
| `make docker-build` | Build both Docker images |
| `make clean` | Remove build artifacts |

## 4.3 Build the web console frontend

The console serves a pre-built Preact/Vite bundle from `webapp/dist`. Build it before
launching `veriproc-console`.

With a system Node.js (or the repo-provided toolchain on `PATH`):

```bash
export PATH="$PWD/.tools/node/bin:$PATH"   # only if using the bundled Node
make webapp-install
make webapp-build
```

The build injects the version and commit (`VITE_APP_VERSION`, `VITE_APP_COMMIT`) so the UI
can display them.

### No system Node.js? Use a user-local toolchain

If you cannot install Node.js system-wide, create one inside a Python virtualenv:

```bash
python3 -m venv .venv
. .venv/bin/activate
python -m pip install --upgrade pip nodeenv
nodeenv -p --node=20.11.1
cd webapp
npm install --no-fund --no-audit
npm run build
cd ..
```

## 4.4 First run (sandbox)

The repository ships ready-to-run sandbox profiles. To start a local instance:

```bash
# Start the daemon
mkdir -p sandbox/data
./bin/veriprocd --config sandbox/instance.yaml
```

In another shell, point the CLI at it:

```bash
export VERIPROC_API_URL="http://localhost:8080"
export VERIPROC_OUTPUT="table"
./bin/veriproc health
./bin/veriproc station list
```

To add the console (after building the webapp):

```bash
./bin/veriproc-console --config sandbox-console/console.yaml
# open http://127.0.0.1:8090/
```

### Bundled sandbox profiles

| Directory | Purpose |
|-----------|---------|
| `sandbox/` | Main local developer instance profile (config, station fixtures, archive layout) |
| `sandbox1/` | An additional local instance profile (for multi-instance demos) |
| `sandbox-console/` | A ready-to-run operator console profile |

These directories are the best starting point for understanding how to structure an
instance configuration and operate VeriProc locally. Continue to
[Configuration Reference](05-configuration.md) for the full set of options.
