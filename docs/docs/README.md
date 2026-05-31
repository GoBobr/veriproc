# VeriProc Documentation

This directory contains the **user and operator documentation** for VeriProc as it is
implemented in this version. It describes what the system does, how to deploy and
operate it, every configuration option, usage examples, design considerations, and
known limitations.

> This is *documentation of the implemented system*, not a specification driving future
> implementation. For the normative specification (the design baseline), see
> [`docs/specs/`](../specs/). Where this documentation and the spec disagree, the
> behavior described here reflects what the code in this repository actually does.

## How to read this

The pages are ordered from "what is it" to "how do I run and operate it". If you are
new, read them in order. If you are looking for a reference, jump straight to the
relevant page.

| # | Page | What it covers |
|---|------|----------------|
| 1 | [Overview](01-overview.md) | What VeriProc is, who it is for, the three binaries, key features |
| 2 | [Architecture](02-architecture.md) | Components, data flow, package map, authoritative records |
| 3 | [Concepts & Lifecycle](03-concepts.md) | Task, run, job, artifact, fingerprint, canonicality, states |
| 4 | [Installation & Build](04-installation-build.md) | Prerequisites, building binaries, building the webapp |
| 5 | [Configuration Reference](05-configuration.md) | Every `instance.yaml` option, defaults, precedence, examples |
| 6 | [Stations & Job Orders](06-stations.md) | Station YAML, context references, input/output, job-order rendering |
| 7 | [Executors](07-executors.md) | local, stub, slurm-native, slurm-docker; runtime environment |
| 8 | [CLI Reference](08-cli.md) | `veriproc` commands, flags, output formats, exit codes |
| 9 | [REST API](09-rest-api.md) | `veriprocd` HTTP endpoints, payloads, error envelope |
| 10 | [Operator Web Console](10-web-console.md) | `veriproc-console` gateway, config, multi-instance UI |
| 11 | [Operations](11-operations.md) | Rolling archives, publication, retries, split groups, cleaning, reconciliation |
| 12 | [Deployment with Docker](12-deployment-docker.md) | Images, compose, volumes, hardening |
| 13 | [Design Considerations & Limitations](13-design-limitations.md) | Why it is built this way, what it does not do |

## Rendering these docs

The pages are plain GitHub-Flavored Markdown with Mermaid diagrams, so they render
directly on GitHub and in most Markdown viewers. They are also structured to drop into a
static-site generator with no rewriting:

- **MkDocs** (Material): point `docs_dir` at `docs/docs` and list the files in `nav`.
- **Hugo**: place the files under `content/docs/` and add front matter if you want
  weighted ordering; the numeric filename prefixes already give a stable order.

No generator is required to use the documentation — it is readable as-is.
