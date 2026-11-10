# hive

Local-first memory for coding agents. Hive captures what an agent did, keeps the
useful parts as typed memories in a local SQLite file, and injects a few short
lines back at session start, on prompts, and when a known error recurs. No API
key, no network, no LLM calls.

Claude Code is supported today. Hive replaces claude-mem for this use.

## Install

Prebuilt archives for darwin and linux (amd64 and arm64) are attached to each
version tag, with `checksums.txt`. `hive version` prints the build.

    go install github.com/a6n-ai/hive/cmd/hive@latest
    hive install --agent claude-code

`install` adds one hook per event to the agent's settings file, keeps every other
hook, and writes a one-time backup beside it. Invalid JSON is refused and left
untouched. `hive uninstall --agent NAME` removes only Hive's hooks. Agents:
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
- Set `HIVE_DISABLED=1` to make hooks do nothing.

Data lives in `$HIVE_HOME/hive.db` (default `~/.hive`). Hooks never fail the
agent: errors are appended to `$HIVE_HOME/hive.log`.

### Index and writer mode

`hive.Options{Index: true}` serves prompt recall from `hive/index`, an in-memory
index of the active and candidate memories (weighted text fields, tag bitmaps
for project, scope, type, and status). SQLite stays the source of truth: the
index is rebuilt from it when needed. It is saved to `$HIVE_HOME/index.snap` on
`Close`, and a snapshot that is missing, stale, or damaged is rebuilt. Writes
through `Hive` keep it current.

`hive.Options{Writer: true}` marks the one process that batches capture (for
example, a gateway). It implies `Index`. `Hive.CaptureBatch` stores a batch in
one transaction; a locked database spools the whole batch, and an event that
fails processing is quarantined in `spool/bad` while the rest commit.

## CLI

    hive recall [--project P] [--query Q] [--budget N]
    hive remember TEXT [--type note|preference|decision] [--scope global|project] [--project P]
    hive forget ID [--purge]
    hive why ID
    hive list [--type T] [--project P] [--status S]
    hive doctor
    hive import claude-code [--since YYYY-MM-DD] [--project P] [--root DIR]
    hive retention apply [--yes]
    hive consolidate (--session S | --catch-up) [--agent A] [--dry-run]
    hive undo RUN_ID
    hive sync --agent claude-code|codex|gemini [--project P] [--include-global]
    hive mcp

`import` backfills events and memories from `~/.claude/projects`; running it
again adds nothing. `retention apply` shows a dry-run count and asks before
deleting; the default `rolling` mode clears prompt text after 30 days and
deletes events after 180 days unless a memory cites them.

Consolidation is opt-in: set `consolidate.auto = true` in `config.toml`. At
session end, your own agent (Claude Code, run as `claude -p` with `haiku`) reviews
the session's new events and proposes merges, promotions, rejections, and
decisions. Each run costs one model call. Every change is audited, and
`hive undo RUN_ID` reverses a run, leaving alone any memory changed since.

`doctor` reports each agent's installed hooks and its warnings, the database and
schema version, a scrubber self-test, and pending spool files. It warns when
claude-mem hooks are still in an agent's settings file.

`sync` writes this project's memories into the agent's instructions file for agents
without hooks. It writes only project and file memories unless `--include-global`
is given, and it refuses symlinks.

## Removing claude-mem

Hive and claude-mem both inject context, so run one. To remove claude-mem's
hooks, delete its entries from the `hooks` section of `~/.claude/settings.json`
(any command containing `claude-mem`), then run `hive doctor` to confirm.

## Development

    make test    # go test -race ./...
    make lint    # golangci-lint run
    HIVE_BENCH=1 go test ./bench -bench . -benchtime 50x

The benchmark seeds 1,000,000 events under `bench/.cache/` and reports the p95
latency of the hook binary, uncontended and with the write lock held.
