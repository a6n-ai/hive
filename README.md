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

## Update

    inscribe update          # script install: fetches the latest release, verifies it, swaps the binary
    brew upgrade inscribe    # Homebrew install (inscribe update tells you this)

Running the install script again does the same as `inscribe update`; it does
nothing when the installed version is already the latest.

## Uninstall

    inscribe uninstall           # hooks for every agent and the MCP server; keeps your data
    inscribe uninstall --purge   # also deletes ~/.inscribe: memories, events, config

Then remove the binary the way you installed it: `brew uninstall inscribe`, or for
the script install, in one step:

    curl -fsSL https://raw.githubusercontent.com/a6n-ai/inscribe/main/install.sh | sh -s -- --uninstall --purge

Drop `--purge` to keep your data. If setup disabled claude-mem, re-enable it with
`claude plugin enable <its id>`.

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
- Token counts, model and message ids (no message text) are read from Claude
  Code transcripts, and stored with the session they belong to.
- Rolling retention and the replay sample are planned for a later release.
- Set `INSCRIBE_DISABLED=1` to make hooks do nothing.
- A session that stops without a `SessionEnd` (a closed window, a crash, a tab
  left idle) is closed at the next session start once it has been silent for an
  hour, or, for Claude Code, once no running process claims it. A later event
  reopens it.
- Set `debug = true` in `config.toml`, or `INSCRIBE_DEBUG=1`, to log what each
  hook event loaded and saved to `inscribe.log`, with up to 3 examples of each.
  Claude Code also shows it in the status line. Debug also notes each feedback ask
  there and logs each answer.

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

    inscribe recall [QUERY] [--project P] [--query Q] [--budget N]
    inscribe remember TEXT [--type note|preference|decision] [--scope global|project] [--project P]
    inscribe forget ID [--purge]
    inscribe why ID
    inscribe list [--type T] [--project P] [--status S]
    inscribe doctor
    inscribe import claude-code [--since YYYY-MM-DD] [--project P] [--root DIR]
    inscribe import claude-mem [--db PATH] [--types decision,bugfix] [--project P] [--since YYYY-MM-DD] [--dry-run]
    inscribe retention apply [--yes]
    inscribe consolidate (--session S | --catch-up) [--agent A] [--dry-run]
    inscribe undo RUN_ID
    inscribe sync --agent claude-code|codex|gemini [--project P] [--include-global]
    inscribe score (train|show [--outcomes]|combine --dir DIR)
    inscribe stats [--days N] [--all] [--explain] [--json]
    inscribe stats ingest --catch-up [--limit N]
    inscribe serve [--addr 127.0.0.1:7331]
    inscribe mcp

`inscribe mcp` serves `remember`, `recall`, `forget`, `why` and `feedback` for the project it
starts in (the git root of its working directory). Every argument except the
text or query is optional. A tool call may name a directory inside that project,
such as a repository under a workspace folder; it resolves to that directory's git
root. A relative path starts at the project. Sibling and parent projects are
refused. `why` prints one short line per evidence event. Recall always merges global
memories, so `remember --scope global` shares a memory across projects.

After a save, Claude Code is asked which recalled memories it used, when at least
4 recalled memories went unasked and no ask was made in the last 10 minutes. Asks
need the inscribe MCP server registered with Claude Code. It answers with
the `feedback` tool: the ask id, then for each memory `used` (yes, partly or no),
`helpful` (yes, no or wrong) and, for a used memory, a short `how`. Answers become
score-model labels. `wrong` also adds a lasting penalty that lowers that memory's
ranking: 0.2 for a project memory, 0.05 for a global one. Session-end consolidation
rates the memories no answered ask covered, only when consolidation runs
(`consolidate.auto` on, at least 20 new events).

`import` backfills events and memories from `~/.claude/projects`; running it
again adds nothing. `import claude-mem` reads decisions and bugfixes from
`~/.claude-mem/claude-mem.db`, opened read-only, as project memories: `decision`
stays `decision` and `bugfix` becomes `fix`. Each imported memory has confidence
0.6, below the 0.9 of `remember`. Observations are skipped when the session has
no cwd, the type is `sensitive`, a path contains `apps/puchkaman`, the title is
empty, or the timestamp is missing. `--dry-run` counts without writing.
`retention apply` shows a dry-run count and asks before
deleting; the default `rolling` mode clears prompt text after 30 days and
deletes events after 180 days unless a memory cites them.

Consolidation is on by default; set `consolidate.auto = false` in `config.toml`
to turn it off. At session end, your own agent (Claude Code, run as `claude -p` with `haiku`) reviews
the session's new events and proposes merges, promotions, rejections, and
decisions. Each run costs one model call. Every change is audited, and
`inscribe undo RUN_ID` reverses a run, leaving alone any memory changed since.

`score train` runs the idle pass that labels finished sessions' injections and
updates the local score weights. It prints the count for each reason and the
weights. `score show --outcomes` also lists the latest 20 groups: each prompt
digest prefix, then one line per row with its reason, label (`-` when unlabeled),
scale, and shown key.

Sharing is off by default. Set `analytics = true` and `url` under `[share]` in
`config.toml`, and after a train commits Inscribe posts one JSON snapshot to that
URL: the install id, version, rule names, the seven weights, and the seen, hits,
and misses counts. It never sends prompts, memory text, paths, commands, or memory
ids. Turning `analytics` off stops uploads and keeps the id. A project
`.inscribe.toml` cannot set `[share]`. `score combine --dir DIR` is a maintainer
command: it reads a directory of snapshots and prints release weight candidates
without editing any source.

`serve` runs a local page on loopback (default `127.0.0.1:7331`). It shows the
score weights, the counts, and the share switch with the share URL's host only.
`--addr` must be `127.0.0.1` or `::1` with a port; anything else is refused. Requests
are accepted only with a `Host` of `127.0.0.1:PORT` or `localhost:PORT`, plus
`[::1]:PORT` when bound on `::1`; any other `Host` is refused. Saving the share switch rewrites `config.toml` and
drops comments.

`doctor` reports each agent's installed hooks and its warnings, the database and
schema version, how many ended Claude Code sessions have missing or unparsed usage,
a scrubber self-test, and pending spool files. It warns when
claude-mem hooks are still in an agent's settings file.

### Stats

`inscribe stats` reports what Inscribe injected and what came of it, for this
project over the last 7 days (`--days N`, `--all` for every project, `--json` for
machine output, `--explain` for the estimate model and its biases).
`inscribe stats ingest --catch-up` reads token usage from the transcripts of
ended Claude Code sessions; Inscribe also spawns it detached at Claude Code session start and end.
Upgrading applies migration 0009, which adds the analytics tables. Older binaries
then refuse the store, so do not downgrade without restoring a backup.

What the numbers mean:

- **Measured** figures are counts: tokens injected per hook, hit and miss
  labels, wrong ratings, feedback asks, memory use, store size, and usage.
  Nothing in that block is a guess.
- **Estimated** tokens saved are a range, from rule-only labels (the low end) to
  every label (the high end). The range is hidden until at least 30 labeled hits
  exist.
- **Holdout** is off by default. With `[stats] holdout` set, that fraction of prompts
  get no injection, and the report compares them with the rest. The verdict is
  `consistent`, `contradicted` or `inconclusive`. The net (saved minus injected)
  is shown only when the verdict is `consistent`.

The `[stats]` keys, in the global `config.toml` only:

| Key | Default | Meaning |
| --- | --- | --- |
| `session_line` | `true` | Show the one-line summary at session start. |
| `holdout` | `0` | Fraction of prompts held out from injection, `0` to `0.5`; `0` is off. |
| `holdout_min_sample` | `100` | Held-out prompts needed before a verdict. |
| `idle_minutes` | `5` | Gap that ends active time in a session. |

`[share] analytics` is a different switch: it controls the upload described
above and has nothing to do with `[stats]`.

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
