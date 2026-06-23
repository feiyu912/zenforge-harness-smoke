# ZenForge Harness Smoke App

> A separate Go project that imports ZenForge as a library and assembles the
> harness outside the framework, deepagents-style.

📖 **The full walkthrough of this project is published as a tutorial on the
ZenForge docs site:
[feiyu912.github.io/zenforge/tutorial](https://feiyu912.github.io/zenforge/tutorial/).**
That page is the authoritative reference; this README is a quick
project-level orientation.

This app demonstrates:

- application-owned model selection;
- a local skill/tool;
- HITL approval;
- a shell tool running through a Docker-backed sandbox adapter owned by this
  app, not ZenForge core.

## Quick Start

For MiniMax, put the key in the environment:

```bash
export ANTHROPIC_API_KEY=...
export ANTHROPIC_BASE_URL=https://api.minimaxi.com/anthropic/v1
go run .
```

Then type your question at the `You:` prompt.

Or copy `.env.example` to `.env`, fill `ANTHROPIC_API_KEY`, then run:

```bash
go run .
```

If `ANTHROPIC_API_KEY` is present, the app automatically uses MiniMax through
the Anthropic-compatible adapter. Without it, the app uses the deterministic
scripted model so the harness can still be tested offline.

## Scripted Local Smoke

This path needs no model API key. It uses a deterministic scripted model that
calls the local skill, asks for shell approval, then runs `uname -a` in Docker.

```bash
go run . --model scripted --approve auto
```

Use `--approve prompt` to answer the HITL request interactively. Use
`--verbose` to print raw tool results and harness events while debugging.

## MiniMax Smoke

MiniMax is Anthropic-compatible in the same style as the Deep Agents demo, so it
uses ZenForge's `anthropic` model adapter with MiniMax's base URL. The app wires
that adapter outside the harness.

```bash
export ANTHROPIC_API_KEY=...
export ANTHROPIC_BASE_URL=https://api.minimaxi.com/anthropic/v1
go run .
```

## Notes

Docker must be available for `--docker=true`. Use `--docker=false` to run the
shell tool locally while keeping the same harness wiring.

## See also

- [Tutorial](https://feiyu912.github.io/zenforge/tutorial/) — full walkthrough
- [Concepts](https://feiyu912.github.io/zenforge/concepts/) — ZenForge mental model
- [Examples](https://feiyu912.github.io/zenforge/examples/) — other example agents
- [Provider guide](https://feiyu912.github.io/zenforge/provider-guide/) — Anthropic / OpenAI / MiniMax
