# @kaixuan/llm-gw-installer

npm entry point for [AI Native Gateway (llm-gateway-go)](https://github.com/halfking/ai-native-gateway-core) — one command, two scales:

| Scale | Storage | Use case |
|-------|---------|----------|
| `lite` | SQLite, single process | single host, intranet, evaluation, CI |
| `full` | PostgreSQL + Redis | production, multi-replica, high concurrency |

This package is a **launcher**, not the gateway itself. It deliberately does not
re-implement download/verify logic (the mandatory sha256 check lives in the
maintain API and the Go installer). It picks the right already-tested path for
your platform:

1. an `llm-gw-installer` binary already on this machine (so `npm i -g` composes
   with `go install` / a release bundle / a source build), or
2. the official installer one-liner for this platform.

## Install from this source tree (no registry needed)

The published registry package may lag behind or not exist yet; installing from
a cloned source tree always works:

```bash
git clone https://github.com/halfking/ai-native-gateway-core.git
cd ai-native-gateway-core
npm install -g ./npm/llm-gw-installer

# same thing, wrapped by the bootstrap (auto-prefers the source tree):
bash install.sh --channel npm --mode lite
```

For a fully source-based install, build the installer binary first — the
launcher picks it up automatically from the install dir:

```bash
bash install.sh build          # go build → ~/llm-gateway/bin/llm-gw-installer
llm-gw-installer install --mode lite
```

## Usage

```bash
llm-gw-installer                        # interactive install (asks lite / full)
llm-gw-installer doctor                 # environment report (offline)
llm-gw-installer version                # version + how to get updates (offline)
llm-gw-installer install --dir <path> --mode lite|full
llm-gw-installer uninstall [--purge]
```

`doctor` / `version` / `--help` are answered by the launcher itself and need no
network — they work immediately after `npm i -g`, before any binary exists.

## Environment

| Variable | Meaning | Default |
|----------|---------|---------|
| `MAINTAIN_BASE` | release entry | `https://llmgateway.internal.example.com/maintain-api` |
| `LLM_GATEWAY_HOME` | install dir | `~/llm-gateway` |

Requires Node.js ≥ 18. Linux / macOS / Windows.
