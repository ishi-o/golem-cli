# golem-cli

The out-of-the-box command-line client for
[golem](https://github.com/ishi-o/golem): a terminal agent on an
OpenAI-compatible model and a SQLite store, with streaming output, sessions,
MCP servers, skills, local scheduling, inline user questions, and cancellation.

```sh
golem config set --api-key your-api-key --model your-model

go run . run "hello"

# Inside a session, /config, /skills, and /mcp are local commands.
go run . session

# Keep the local scheduler alive after a short-lived command exits.
go run . daemon
```

The current directory is the default workspace. On the first interactive
agent command, golem asks for trust and saves approved workspaces in
`~/.config/golem/config.json`.

While an interactive run is thinking or using tools, press Enter to queue a
follow-up message in the same Golem conversation.

Enable session-ID completion with:

```sh
source <(golem completion zsh) # or: golem completion bash
```
