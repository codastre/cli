# codastre CLI

The developer-facing CLI for [Codastre](https://codastre.com) — topology-aware hybrid
retrieval and a knowledge graph over your git repositories, exposed to coding agents via MCP.

The CLI computes git diffs locally, masks paths before they leave your machine, holds
masking keys in your OS keychain, and hydrates snippets from local disk. It never ships
raw code or computes embeddings.

> This repository is a **read-only mirror**. Development happens in the Codastre monorepo;
> the `cli/` subtree is mirrored here so the module path matches and releases are published.

## Install

**Prebuilt binary (recommended).** Download the archive for your platform from the
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

Each release also ships `checksums.txt` for verification (`sha256sum -c`).

**With Go ≥ 1.22:**

```bash
go install github.com/codastre/cli/cmd/codastre@latest
```

## Usage

```bash
codastre login --server https://codastre.your-domain.com   # device-code auth
codastre dashboard                                          # open the dashboard, signed in
codastre serve                                              # stdio MCP proxy for your agent
codastre doctor                                             # diagnostics
```

The dashboard URL is auto-discovered from the server, so no extra configuration is needed.

## License

Licensed under the [Apache License 2.0](LICENSE) — see [NOTICE](NOTICE).
