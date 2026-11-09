# hive

Local-first memory for coding agents. Hive captures what an agent did, keeps the
useful parts as typed memories in a local SQLite file, and injects a few short
lines back at session start, on prompts, and when a known error recurs. No API
key, no network, no LLM calls.

Claude Code is supported today. Hive replaces claude-mem for this use.

## Install

    go install github.com/a6n-ai/hive/cmd/hive@latest
    hive install --agent claude-code

`install` adds one hook per event to `~/.claude/settings.json`, keeps every other
hook, and writes a one-time backup to `settings.json.hive-backup`. Invalid JSON is
refused and left untouched. `hive uninstall --agent claude-code` removes only
Hive's hooks.

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

## CLI

    hive recall [--project P] [--query Q] [--budget N]
    hive remember TEXT [--type note|preference|decision] [--scope global|project] [--project P]
    hive forget ID [--purge]
    hive why ID
    hive list [--type T] [--project P] [--status S]
    hive doctor

`doctor` reports installed hooks, the database and schema version, a scrubber
self-test, and pending spool files. It warns when claude-mem hooks are still in
`~/.claude/settings.json`.

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
