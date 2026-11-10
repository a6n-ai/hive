# inscribe

Local-first memory for coding agents. Inscribe captures what an agent did, keeps the
useful parts as typed memories in a local SQLite file, and injects a few short
lines back at session start, on prompts, and when a known error recurs. No API
key, no network, no LLM calls.

Claude Code is supported today. Inscribe replaces claude-mem for this use.

## Install

Prebuilt archives for darwin and linux (amd64 and arm64) are attached to each
version tag, with `checksums.txt`. `inscribe version` prints the build.

Pick one:

    curl -fsSL https://raw.githubusercontent.com/a6n-ai/inscribe/main/install.sh | sh
    brew install a6n-ai/tap/inscribe
    go install github.com/a6n-ai/inscribe/cmd/inscribe@latest
    # or download inscribe_<version>_<os>_<arch>.tar.gz from the release page

Then run the guided installer:

    inscribe setup

`inscribe setup` detects the agents on your machine and walks through the choices:
which agents to hook, whether to disable claude-mem, how much history to import,
retention, consolidation (off by default), and an MCP server entry at user scope, so every
project sees it. It shows
every file and command before it runs. `inscribe setup --yes` applies the defaults
without prompts; without a terminal it prints the plan and exits 2.

### Advanced

`install` adds one hook per event to the agent's settings file, keeps every other
hook, and writes a one-time backup beside it. Invalid JSON is refused and left
untouched. `inscribe uninstall --agent NAME` removes only Inscribe's hooks. Agents:
`claude-code` (default settings `~/.claude/settings.json`), `codex`
(`~/.codex/hooks.json`) and `gemini` (`~/.gemini/settings.json`). See
[docs/agents.md](docs/agents.md) for what each one supports.

## Privacy defaults

- Every text field is scrubbed (tokens, keys, URL credentials, assignments,
  high-entropy strings) before it reaches the database.
- Prompt text is truncated to 2,000 characters. Error text keeps the first two
  lines, up to 500 characters.
- File contents are never stored. Paths of `.env*`, `*.pem`, `id_*` and
  `*credentials*` files are kept, nothing else of them.
- Rolling retention and the replay sample are planned for a later release.
- Set `INSCRIBE_DISABLED=1` to make hooks do nothing.

Data lives in `$INSCRIBE_HOME/inscribe.db` (default `~/.inscribe`). Hooks never fail the
agent: errors are appended to `$INSCRIBE_HOME/inscribe.log`.

### Index and writer mode

`inscribe.Options{Index: true}` serves prompt recall from `inscribe/index`, an in-memory
index of the active and candidate memories (weighted text fields, tag bitmaps
for project, scope, type, and status). SQLite stays the source of truth: the
index is rebuilt from it when needed. It is saved to `$INSCRIBE_HOME/index.snap` on
`Close`, and a snapshot that is missing, stale, or damaged is rebuilt. Writes
through `Inscribe` keep it current.

`inscribe.Options{Writer: true}` marks the one process that batches capture (for
example, a gateway). It implies `Index`. `Inscribe.CaptureBatch` stores a batch in
one transaction; a locked database spools the whole batch, and an event that
fails processing is quarantined in `spool/bad` while the rest commit.

## CLI

    inscribe recall [--project P] [--query Q] [--budget N]
    inscribe remember TEXT [--type note|preference|decision] [--scope global|project] [--project P]
    inscribe forget ID [--purge]
    inscribe why ID
    inscribe list [--type T] [--project P] [--status S]
    inscribe doctor
    inscribe import claude-code [--since YYYY-MM-DD] [--project P] [--root DIR]
    inscribe retention apply [--yes]
    inscribe consolidate (--session S | --catch-up) [--agent A] [--dry-run]
    inscribe undo RUN_ID
    inscribe sync --agent claude-code|codex|gemini [--project P] [--include-global]
    inscribe mcp

`import` backfills events and memories from `~/.claude/projects`; running it
again adds nothing. `retention apply` shows a dry-run count and asks before
deleting; the default `rolling` mode clears prompt text after 30 days and
deletes events after 180 days unless a memory cites them.

Consolidation is opt-in: set `consolidate.auto = true` in `config.toml`. At
session end, your own agent (Claude Code, run as `claude -p` with `haiku`) reviews
the session's new events and proposes merges, promotions, rejections, and
decisions. Each run costs one model call. Every change is audited, and
`inscribe undo RUN_ID` reverses a run, leaving alone any memory changed since.

`doctor` reports each agent's installed hooks and its warnings, the database and
schema version, a scrubber self-test, and pending spool files. It warns when
claude-mem hooks are still in an agent's settings file.

`sync` writes this project's memories into the agent's instructions file for agents
without hooks. It writes only project and file memories unless `--include-global`
is given, and it refuses symlinks.

## Removing claude-mem

Inscribe and claude-mem both inject context, so run one. To remove claude-mem's
hooks, delete its entries from the `hooks` section of `~/.claude/settings.json`
(any command containing `claude-mem`), then run `inscribe doctor` to confirm.

## Development

    make test    # go test -race ./...
    make lint    # golangci-lint run
    INSCRIBE_BENCH=1 go test ./bench -bench . -benchtime 50x

The benchmark seeds 1,000,000 events under `bench/.cache/` and reports the p95
latency of the hook binary, uncontended and with the write lock held.
