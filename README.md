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
```
