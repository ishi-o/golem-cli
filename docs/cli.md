# CLI

golem-cli builds a terminal agent on an OpenAI-compatible model and a SQLite
store — with streaming output, sessions, MCP servers, skills, scheduling,
inline user questions, and cancellation. No code required.

```sh
# Store these once, or export OPENAI_API_KEY and OPENAI_MODEL instead.
golem config set --api-key your-api-key --model your-model
# export OPENAI_BASE_URL=https://your-compatible-endpoint/v1

golem run "hello"
```

## Commands

```sh
golem run "plan a trip to Kyoto"   # one streamed run
golem session                       # create a new random-id session
golem session SESSION_ID            # resume a listed session and show its history
golem session list                  # list persisted sessions
golem cancel trip-1                 # abort a run in flight
golem mcp add filesystem http://127.0.0.1:3000/mcp
golem mcp list
golem skills
golem config show
golem version
```

`chat` remains an alias for `run`. Both commands fire a `ChatScenario` as user
`local`; `session` repeats those runs with one generated conversation id. Pass
an id previously returned by `session` or `session list` to resume it. The CLI
prints the stored transcript before accepting the next message. The ask tool
is offered with a terminal handler, so the model can put questions to you and
continue from your answers in the same run.

While `session` is open, management commands can be run without sending them
to the model:

```text
/config                 # show local configuration
/config set --model ...
/skills                 # list skills in the active workspace
/mcp                    # list configured MCP servers
/mcp add NAME URL
/exit
```

The session id is also the golem conversation id. Its user, assistant, and
tool messages are stored in SQLX's `golem_chat_memory` table by default, so
resuming the same session id shows its previous transcript and loads its
context. Slash commands are local control input and are not added to the
conversation. Configuration changes take effect on the next process; MCP
changes are synchronized to the live SQLX registry and take effect on the next
session turn.

## Interactive terminal

On a TTY, `run`, `session`, and the default command use a Bubble Tea editor and
viewport. Enter sends the message; `Ctrl-J` or `Alt-Enter` inserts a newline.
Tool calls, skills, reasoning, and subagents are foldable: use `Tab`/`Shift-Tab`
to focus one, `Ctrl-O` to toggle it, and `Alt-O` to toggle all. `PageUp`/
`PageDown` or `Ctrl-Up`/`Ctrl-Down` scroll the transcript, and `Ctrl-L` returns
to the latest output.

## What is wired

- Golem's built-in families (file, memory, skill, todo, ask, publish, clock,
  subagent) are injected automatically; schedule tools are added when the
  local scheduler starts, and Docker shell tools when the sandbox is enabled.
- SQLite persistence under `GOLEM_STORAGE_LOCATION` (default `data`), or
  `GOLEM_SQLITE_PATH` for the database file itself.
- Session conversation history is persisted by golem's SQLX store in its
  `golem_chat_memory` table by default and is loaded again when the same
  session id is opened.
- Configured Streamable HTTP MCP servers are discovered per run; use
  `GOLEM_MCP_TRUSTED_HOSTS` for non-loopback plain HTTP endpoints. Eino's
  official `tool_search` tool is injected alongside the namespaced MCP tools.
- The shell sandbox when `GOLEM_SANDBOX` is set — the same env contract as
  the [configuration reference](configuration.md#sandbox-golem_sandbox).
- Schedule tools and a local scheduler are injected automatically. The
  scheduler lives in the CLI process and persists task definitions in SQLite;
  keep `golem session` running when a task must fire while you are away.

See the [configuration reference](configuration.md) for every variable.
