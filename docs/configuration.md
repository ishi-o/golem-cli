# Configuration

Two configuration surfaces exist, one per shape of using golem:

- **As a library**, you populate the `core/config` structs from any source
  and call `Normalize` — no environment variable is read.
- **The CLI** reads the variables below through `bootstrap` and uses the
  current directory as its default local workspace.

The CLI also reads the local settings file shown by `golem config path`.
Environment variables override values from that file, which makes the same
binary convenient for both local use and CI.

By default the file is `~/.config/golem/config.json`; set `GOLEM_CONFIG_FILE`
to use another path.

Use `golem config set --api-key ... --model ...` to write the local settings
file. It is created with mode `0600`; `golem config show` masks the key.

The first interactive agent command asks you to trust its workspace. The
approved absolute directories are saved as `trusted_directories` in the same
file. Piped and other non-interactive commands refuse an untrusted workspace
instead of approving it silently.

## Model and store

| Variable | Purpose |
| --- | --- |
| `OPENAI_API_KEY` | API key for the CLI model |
| `OPENAI_MODEL` | Model name used by the CLI |
| `OPENAI_BASE_URL` | Optional OpenAI-compatible API base URL |
| `GOLEM_SQLITE_PATH` | Optional SQLite database path (default `<workspace>/golem.db`) |

## Runtime (`GOLEM_*`)

| Variable | Purpose |
| --- | --- |
| `GOLEM_LOCALE` | Language used by agent-generated runtime messages |
| `GOLEM_STORAGE_LOCATION` | Trusted project directory used as the local workspace; defaults to the current directory |
| `GOLEM_STORAGE_BASE_URL` | Base URL for published files |
| `GOLEM_STORAGE_CDN_URL` | Optional CDN base URL for published files |
| `GOLEM_ADMINS` | Comma-separated administrator IDs |
| `GOLEM_ASK_USER_ENABLED` | Offer the ask tool (default true) |
| `GOLEM_ASK_USER_TTL` | Question lifetime as a Go duration |
| `GOLEM_PUBLISH_BASE_URL` | Base URL emitted by the publish-file tool |
| `GOLEM_GUIDE_THRESHOLD` | Tool-result size threshold for file-backed responses |
| `GOLEM_MCP_TRUSTED_HOSTS` | Comma-separated MCP host allowlist |

MCP servers can be registered without editing JSON:

```sh
golem mcp add my-tools https://tools.example.com/mcp \
  --header 'Authorization=Bearer your-token'
golem mcp list
golem mcp remove my-tools
```

Only Streamable HTTP MCP servers are supported. Loopback HTTP endpoints are
allowed for local development; other plain HTTP hosts must be listed in
`GOLEM_MCP_TRUSTED_HOSTS`.

## Sandbox (`GOLEM_SANDBOX*`)

`GOLEM_SANDBOX` selects the shell sandbox backend: `docker` or unset — no
shell tools. Misconfiguration (a backend selected without its required
variables) fails startup rather than silently offering nothing.

| Variable | Purpose |
| --- | --- |
| `GOLEM_SANDBOX` | Backend: `docker` or unset (off) |
| `GOLEM_SANDBOX_IMAGE` | Sandbox container image (required when a backend is set) |
| `GOLEM_SANDBOX_NETWORK` | Docker network for sandbox containers (docker only) |

Docker sandboxes additionally honour the standard Docker environment
(`DOCKER_HOST`, TLS variables).

After workspace trust is granted, the CLI creates the ignored
`.golem/runtime/local` symlink to the project directory. This is the owner
home path expected by Golem v1 and lets Docker mount the trusted project as
the sandbox working directory. The existing `<workspace>/local` directory, if
present, is left untouched.

The CLI starts a local scheduler automatically, so the schedule tools are
available without another service. The scheduler is process-local: keep
`golem session` open or run `golem daemon` for tasks that must fire after a
short-lived command exits. Use `bootstrap.WithoutScheduler()` when embedding
the bootstrap package and a long-lived scheduler is not wanted.
