# golem-cli

The out-of-the-box command-line client for
[golem](https://github.com/ishi-o/golem): a terminal chat agent on an
OpenAI-compatible model and a SQLite store, with streaming output, inline
user questions, and cancellation.

```sh
export OPENAI_API_KEY=your-api-key
export OPENAI_MODEL=your-model

go run . chat "hello"
```

## Layout

- `main.go` — entry point; loads config, bootstraps the runtime, runs Cobra.
- `cli/` — the command tree (chat, cancel, version) and terminal listeners.
- `bootstrap/` — env-driven runtime assembly: model, SQLite store, sandbox.
- `docs/` — the [CLI guide](docs/cli.md) and the
  [configuration reference](docs/configuration.md).

## Local development

`go.work` points at a sibling checkout of `golem`
(`../golem/core`, `../golem/store/sqlx`, `../golem/sandbox/...`), so edits
to the library are picked up immediately. Without it, the `golem` modules
resolve from their published versions.
