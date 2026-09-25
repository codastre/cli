<div align="center">

# `codastre` CLI

**Topology-aware hybrid retrieval and a knowledge graph for your codebases — in your coding agent.**

Semantic + lexical code search, cross-repo relationship graphs, and branch-aware
sync, exposed to Claude Code, Codex, and opencode over [MCP](https://modelcontextprotocol.io).

[![Release](https://img.shields.io/github/v/release/codastre/cli?sort=semver&label=release&color=0d9488)](https://github.com/codastre/cli/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/codastre/cli?label=go&color=00ADD8)](go.mod)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)
![Platforms](https://img.shields.io/badge/platforms-macOS%20%C2%B7%20Linux%20%C2%B7%20Windows-555)
![MCP](https://img.shields.io/badge/MCP-stdio%20%C2%B7%20streamable--http-5eead4)

[Install](#-install) · [Quickstart](#-quickstart) · [Commands](#-commands) · [Privacy](#-privacy-by-design) · [codastre.com](https://codastre.com)

</div>

---

`codastre` is a single static binary with no runtime dependencies. It runs on the
machine where your coding agent lives and gives that agent three things a plain
file-tree never could:

- **🔍 Hybrid retrieval** — dense semantic embeddings *and* BM25 lexical search, fused
  with Reciprocal Rank Fusion, so exact identifiers and fuzzy intent both land.
- **🕸 Knowledge graph** — cross-repo edges (Kafka producer/consumer, HTTP/gRPC calls,
  shared packages) extracted with tree-sitter, traversable in a single request.
- **🌿 Branch-aware sync** — a local HEAD watcher diffs your working branch and ships
  only what changed, so results stay fresh without re-indexing the world.

The CLI computes git diffs locally, **masks paths before they leave your machine**,
holds masking keys in your OS keychain, and hydrates snippets from local disk. It
never ships raw source and never computes embeddings.

> [!NOTE]
> This repository is a **read-only mirror**. Development happens in the Codastre
> monorepo; the `cli/` subtree is mirrored here so the Go module path resolves and
> releases are published.

## 📦 Install

**Prebuilt binary** *(recommended)* — grab the archive for your platform from the
[latest release](https://github.com/codastre/cli/releases/latest), extract, and put
`codastre` on your `PATH`:

```bash
# macOS (Apple Silicon) — adjust VERSION/OS/ARCH to match the asset names
VERSION=0.1.0; OS=darwin; ARCH=arm64
curl -fsSL "https://github.com/codastre/cli/releases/download/v${VERSION}/codastre_${VERSION}_${OS}_${ARCH}.tar.gz" \
  | tar -xz codastre
sudo mv codastre /usr/local/bin/
codastre version
```

Every release ships a `checksums.txt` — verify your download with `sha256sum -c`.

**From source** — with Go ≥ 1.22:

```bash
go install github.com/codastre/cli/cmd/codastre@latest
```

## 🚀 Quickstart

Three steps from clone to context:

```bash
# 1 — Authenticate (RFC 8628 device code; key lands in your OS keychain)
codastre login

# 2 — Tell codastre where your clones live, so results carry source
codastre checkout scan ~/src

# 3 — Register codastre with your coding agent over MCP
codastre connect claude        # also: codastre connect codex · opencode
```

That's it — your agent now has `QUERY`, `GRAPH`, `SYNC`, and `REGISTER` tools, and a
HEAD watcher keeps the active branch in sync as you work. Point at a self-hosted
server with `--server https://codastre.your-domain.com` (or `$CODASTRE_SERVER`); the
dashboard URL is auto-discovered, so there's nothing else to configure.

Step 3 makes Codastre *available* to your agent. An **integration** makes it *reach for
it* — ready-made commands, skills that load themselves on search and structural
questions, and hooks that steer the agent off `grep`. Claude Code has one today, shipped
as a plugin:

```bash
claude plugin marketplace add codastre/integrations
claude plugin install codastre@codastre-plugins --scope project
```

`codastre connect claude` prints those two lines for you, pointing at whichever source
your server publishes (a self-hosted deployment usually mirrors the integrations into an
internal marketplace). The plugin ships its own `codastre serve` MCP entry, so it also
covers step 3. Codex and opencode have no integration yet — they get the same tools over
MCP from step 3 alone.

Step 2 is what makes a hit show code instead of just naming a file. Snippets are
hydrated from a local clone, so a repo codastre can't find on disk still matches
searches but comes back as bare `path:line` locators. One `scan` of the directory your
clones sit under covers every repo at once — `codastre checkout list` shows the result,
and `codastre doctor` warns when the current repo isn't registered.

Prefer to look before you connect? Search straight from the terminal — no MCP wiring
required:

```bash
codastre query "where do we validate webhook signatures"
codastre graph PaymentService.charge --kind calls --depth 2
```

## 🛠 Commands

| Command | What it does |
| --- | --- |
| `codastre login` | Authenticate via RFC 8628 device-code flow; stores the key in your OS keychain |
| `codastre connect <target>` | Register codastre as an MCP server in `claude`, `codex`, or `opencode` |
| `codastre checkout scan [root]` | Find every clone under a root and register them all (defaults to the parent of the current repo) |
| `codastre checkout list` | List registered checkouts and flag stale entries (`add`, `remove`, `prune` round out the group) |
| `codastre serve` | Start the stdio MCP proxy (and HEAD watcher, unless `--no-watch`) |
| `codastre sync` | Watch HEAD and sync on changes (`--once` for a single eager sync, then exit) |
| `codastre query <text>` | Hybrid semantic + lexical code search — no MCP connection required |
| `codastre graph <symbol>` | Traverse the cross-repo relationship graph from a symbol or chunk |
| `codastre masking-key` | Copy a repo's HMAC masking key to the clipboard (hex) |
| `codastre savings` | Summarise your own search-tool usage from the local log (no server call) |
| `codastre dashboard` | Open the web dashboard in an already-authenticated session |
| `codastre doctor` | Run diagnostics — exit `0` = all pass, `1` = error, `2` = warnings only |
| `codastre logout` | Revoke the stored API key server-side and remove it from the keychain |
| `codastre version` | Print the CLI version |

Run `codastre <command> --help` for flags and details.

## 📊 What it cost you — `codastre savings`

```bash
codastre savings              # last 30 days
codastre savings --window 7d
codastre savings --window all --json
```

Reads the local JSONL log the Claude Code plugin writes when
`CODASTRE_TRACK_TOKENS=1` (or while a live A/B mode is on) and groups it by what
ran: codastre on the MCP plane, codastre on the CLI plane, text search, and file
reads — plus sessions, workspaces and active days, and the codastre-vs-grep call
ratio in whichever direction your log points.

```
  Codastre (MCP QUERY/GRAPH)   180 calls   ~573,277 tok
  Text search (grep/glob)      434 calls   ~242,484 tok
  File reads                   172 calls   ~223,703 tok
  Codastre (CLI plane)          47 calls    ~92,712 tok
  TOTAL                        833 calls  ~1,132,176 tok
```

Two things it deliberately does not do. **It never leaves the machine** — no server
call, no upload, no opt-in beyond the one that wrote the log, and it prints counters
only, never a logged query string or file path. And **it reports no savings figure**:
there is no observable counterfactual for a search that never ran, so none is computed.
Token counts are byte-ratio estimates (±20%), exclude reasoning tokens, and are not
billing-grade. `codastre doctor` states in one line whether logging and upload are on.

## 🔒 Privacy by design

The server is never trusted with your source. The CLI enforces that boundary:

- **Paths are masked, not sent in the clear.** File paths are HMAC-masked locally
  before any request leaves the machine; the server stores and returns only masked
  path tokens, line ranges, and scores — never code.
- **Keys stay in your keychain.** Your API key and per-repo masking keys live in the
  OS keychain (Keychain on macOS, Secret Service on Linux, Credential Manager on
  Windows) — never in a shell history, env file, or URL.
- **Diffs are computed locally.** Branch sync sends `git diff` tuples with masked
  paths; only cache-miss blobs are processed server-side, and snippets are hydrated
  from your local disk at display time.
- **No embeddings on your machine, no raw code off it.** Embedding happens server-side
  against the provider you choose; the bytes of your files never leave.

Path masking does not defend against a fully compromised control plane — the
dashboard's security-posture view is explicit about exactly where the guarantee ends.

## ⚙️ Configuration

| Variable / flag | Purpose |
| --- | --- |
| `CODASTRE_SERVER` / `--server` | Codastre server URL (defaults to the managed service) |
| `CODASTRE_API_KEY` / `--key` | API key override; takes precedence over the keychain |
| `CODASTRE_TRACK_TOKENS` | Set to `1` to have the Claude Code plugin log search-tool usage locally |
| `CODASTRE_TOKEN_LOG` | Override the log path (default `~/.config/codastre/claude-token-log.jsonl`) |

## 🔗 Learn more

- 🌐 **[codastre.com](https://codastre.com)** — what it is and how it works
- 📥 **[codastre.com/install](https://codastre.com/install)** — install + connect walkthrough
- 📦 **[Releases](https://github.com/codastre/cli/releases)** — binaries and changelogs

## 📄 License

Licensed under the [Apache License 2.0](LICENSE) — see [NOTICE](NOTICE).
