# Sandbox — Operator Web Console

This deployment runs the VeriProc Operator Web Console against the existing
[`sandbox1`](../sandbox1/README.md) daemon. The console is a separate
process that polls one or more upstream veriprocd instances and serves a
browser UI; nothing in `sandbox1/` is modified.

## Prerequisites

- A working Go toolchain.
- Node.js 18+ on `PATH` (a pre-staged copy lives at
  `../.tools/node/bin` if your system lacks one — `export
  PATH=$PWD/../.tools/node/bin:$PATH`).
- Both binaries built: `make build` from the workspace root.
- Frontend bundle built: `make webapp-install webapp-build`.

## Launch

```sh
# 1. From the workspace root, start the upstream sandbox daemon:
bin/veriprocd --config sandbox1/instance.yaml

# 2. In another shell, start the console gateway:
bin/veriproc-console --config sandbox-console/console.yaml
```

The gateway binds on `127.0.0.1:8090` and serves both the JSON API at
`/api/console/*` and the Preact bundle at `/`. Open
<http://127.0.0.1:8090/> in a browser.

## Tokens

`console.yaml` declares two static tokens for sandbox use only:

| role     | subject         | token                    |
| -------- | --------------- | ------------------------ |
| viewer   | `viewer-demo`   | `sandbox-viewer-token`   |
| operator | `operator-demo` | `sandbox-operator-token` |

Sign in by pasting the token, declaring a subject and selecting the role.
The role you pick is only used to hide mutating menu items in the UI;
the gateway re-checks the token's role on every mutating endpoint
(`pause`, `unpause`, `submissions`, `cancel`, `hide`, `retry`).

## What you should see

- One Instance block titled **Sandbox (local)**.
- One row per station configured in `sandbox1/stations/`.
- Slots arranged running → completed → queued → failed, with empty
  padding up to twelve slots and a `+N` overflow indicator.
- Left-clicking a non-empty slot opens that task's run browser in a new
  tab; right-clicking offers cancel / retry / hide / open actions.
- The task page lists every retry of a task and lets you walk its working
  root directory tree with inline text/log preview (binary files render a
  placeholder).

## Files

- `console.yaml` — gateway configuration (HTTP bind, UI defaults, DB
  DSN, static tokens, instance list).
- `console.db` — created on first launch; SQLite state for tokens,
  hidden runs, instance preferences, and audit log.

## Reset

```sh
rm -f sandbox-console/console.db
```

This drops the audit log and any hidden-run records but leaves
`console.yaml` and `sandbox1/` untouched.
