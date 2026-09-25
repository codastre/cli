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
| `codastre collect` | Parse local Claude Code transcripts into usage counters (local; `--upload` is opt-in) |
| `codastre savings` | Summarise your own search-tool usage — transcripts, the local log, or the server |
| `codastre study` | Take part in (or, as an admin, read and judge) a pre-registered paired study — tool vs no tool |
| `codastre dashboard` | Open the web dashboard in an already-authenticated session |
| `codastre doctor` | Run diagnostics — exit `0` = all pass, `1` = error, `2` = warnings only |
| `codastre logout` | Revoke the stored API key server-side and remove it from the keychain |
| `codastre version` | Print the CLI version |

Run `codastre <command> --help` for flags and details.

## 📊 What it cost you — `codastre collect` + `codastre savings`

```bash
codastre collect              # parse the transcripts already on this machine
codastre savings              # last 30 days
codastre savings --window 7d
codastre savings --source server   # your own rows from the control plane
codastre savings --window all --json
```

Two sources, reported one at a time and **never summed**:

- **transcripts** (`codastre collect`) — Claude Code's own session records, already
  on disk. Exact tokens, measured USD, wall-clock and tool time, per-tool result
  bytes, and one search episode per turn. Incremental: each file resumes from a
  watermark, so a re-run with nothing appended reads nothing.
- **the plugin log** — byte-ratio estimates (±20%), but the only source that covers
  Cursor, Codex and other MCP clients. Written when `CODASTRE_TRACK_TOKENS=1` or
  while a live A/B mode is on.

- **the server** (`--source server`) — `GET /v1/me/usage`, the control plane's count
  of your own calls (calls, envelope tokens, repos touched ≥2), printed with the
  server's own receipt verbatim so the CLI and the dashboard cannot explain the same
  number two different ways. Opt-in: it is the only source that makes a network call.

`--source auto` (the default) prefers the transcript when a collection exists and
falls back to the log, and never calls the server; `--source transcript|log|server`
pins it.

```
Sessions
  159 sessions · 885 turns · 16 projects · 20 active days
  $748.61 measured spend · 1744.1 h wall clock · 5.4 h in tools

Context
  re-read amplification 96% = 1,225,649,437 / (1,225,649,437 + 40,252,761 + 5,997,126)

Search episodes (one per turn)
  codastre only                17 ←
  fallback after codastre      47
  codastre failed               3
  text search only            282

  answered without fallback: 17 / 67 = 25.4%
```

Both failure rows and the denominator are always shown: a rate that hides its
denominator reads as marketing.

Three things these commands deliberately do not do. **Nothing leaves the machine**
unless you ask — the local sources make no server call and no upload, `--source
server` only *reads back* rows the server already had, and the parse of a
transcript keeps counts, byte totals and timestamps only. Prompts, code, paths and tool output are never
stored or printed, which tests assert directly. **No savings figure** is reported:
there is no observable counterfactual for a search that never ran, so none is
computed. And **sources are never mixed** — exact transcript counts and estimated
log tokens answer different questions, and a run says which one it used.
`codastre doctor` states in one line whether logging, collection and upload are on,
and fails if any OTel content-logging variable is set.

### Sharing your counters — `codastre collect --upload` (opt-in)

```bash
export CODASTRE_USAGE_REPORT=1   # the opt-in; without it --upload refuses to run
codastre collect --upload
```

Upload is opt-in twice — the variable *and* the flag — and sends **counters only**:
one row per search turn (outcome, call counts, result bytes, two timestamps) and one
row per finished session (measured cost, token split, durations, per-tool call and
byte counts, context compactions). There is no free-text field on the wire; the
session id is replaced by an HMAC under your tenant's usage key
(`GET /v1/me/usage/session-key`), and your cwd, project, branch, prompts, paths and
tool output never leave the machine — tests assert the upload body against a key
allowlist.

- **Consent boundary.** The first upload-enabled run records `report_since`; sessions
  that started earlier stay local. History from before it is never swept up by the flag.
- **Incremental.** Each run sends only new episodes and sessions whose counters changed;
  the server is idempotent, so a retry is harmless.
- **Compactions** come from the plugin's `PreCompact` hook log
  (`~/.config/codastre/session-events.jsonl`); a transcript does not record them.
- `codastre savings --source server` then shows your episode outcomes beside your
  call counts — each with its own receipt, never combined.
- **Study sessions** (below) are reported under their assignment even if they started
  before the boundary — joining the study was the consent for that session — but still
  only with the variable and the flag.

### History from before the boundary — `codastre collect --backfill`

```bash
codastre collect --backfill                        # preview: prints exactly what would be sent
CODASTRE_USAGE_REPORT=1 codastre collect --backfill --upload   # asks you to type "yes"
```

On its own `--backfill` sends nothing: it lists each pre-boundary session with the
counters that would go on the wire (`--json` prints the rows verbatim, with the
session id shown as `hmac-sha256(<tenant usage key>, <session id>)` — the key is not
fetched for a preview). With `--upload` it asks for an explicit `yes` on a terminal,
and refuses off a terminal unless you pass `--yes`. **Per run:** nothing is remembered
as consent, the boundary does not move, and the next backfill asks again. Backfilled
rows seed your session history (cost, context amplification, compactions); they never
join a study pair, because a session that predates a study's registration was not run
under its pre-registered prompt.

## 🧪 Paired studies — `codastre study`

The strongest measurement codastre has: the same pre-registered task run once with
the tool and once without, both sessions measured exactly from their transcripts.

```bash
codastre study list                       # open studies and their tasks (prompt hashes)
codastre study start auth-q4 --task refresh   # get an arm; writes ~/.config/codastre/study.json
#   → open a NEW Claude Code session and send any message: the plugin injects the
#     pre-registered prompt verbatim and enforces the arm (no_tool blocks Codastre on
#     both the MCP tools and the CLI)
CODASTRE_USAGE_REPORT=1 codastre collect --upload   # reports the session, tagged
codastre study status                     # assignment id (label your diff with it)
codastre study stop                       # end the run; your own search mode is back
```

The server assigns the arm and balances arm order; the client never states its own
arm, only the assignment id and the hash of the prompt it ran. Admins read and judge:

```bash
codastre study show auth-q4               # pairs as rows, n vs target, balance, receipt
codastre study judge auth-q4              # blind queue: acceptance criteria, no arm
codastre study verdict auth-q4 <assignment-id> correct|incorrect
```

`show` never averages: it prints each pair, and the headline — counts of pairs where
each arm was correct, cheaper, faster — appears only once the pre-registered number of
complete, judged pairs exists. Until then it prints why it is withheld.

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
| `CLAUDE_PROJECTS_DIR` | Transcript root for `codastre collect` (default `~/.claude/projects`) |
| `CODASTRE_COLLECT_STATE` | Collected-counter state file (default `~/.config/codastre/collect-state.json`) |
| `CODASTRE_SESSION_EVENTS_LOG` | Hook event log read by `codastre collect` (default `~/.config/codastre/session-events.jsonl`) |
| `CODASTRE_USAGE_REPORT` | Set to `1` to allow `codastre collect --upload` to send counters to the server |
| `CODASTRE_STUDY_FILE` | Active study run written by `codastre study start` (default `~/.config/codastre/study.json`) |
| `CODASTRE_STUDY_LOG` | Study claims written by the plugin hook, read by `codastre collect` (default `~/.config/codastre/study-sessions.jsonl`) |

## 🔗 Learn more

- 🌐 **[codastre.com](https://codastre.com)** — what it is and how it works
- 📥 **[codastre.com/install](https://codastre.com/install)** — install + connect walkthrough
- 📦 **[Releases](https://github.com/codastre/cli/releases)** — binaries and changelogs

## 📄 License

Licensed under the [Apache License 2.0](LICENSE) — see [NOTICE](NOTICE).
