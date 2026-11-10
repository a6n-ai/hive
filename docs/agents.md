# Agents

Hive hooks into three coding agents. Each has its own settings file and its own
hook payloads. This page records what Hive relies on and how it was checked.

## Claude Code

- Settings: `~/.claude/settings.json`. Verified live against Claude Code.
- Hook events: SessionStart, UserPromptSubmit, PostToolUse, PostToolUseFailure,
  Stop, SessionEnd. Hive installs no PreToolUse hook.
- Consolidation runs here only: `claude -p` with tools disabled.

## Codex

- Settings: `~/.codex/hooks.json`, with events under a top-level `hooks` object.
  Verified on Codex 0.163.0 (2026-11-10) and the Codex hook documentation.
- Hook events installed: SessionStart, UserPromptSubmit, PostToolUse, Stop,
  SessionEnd. Hive installs no PreToolUse hook: it never blocks a tool call.
- After `hive install --agent codex`, open `/hooks` in Codex and approve the
  Hive hooks. Codex does not run a new or changed hook until it is approved.
- Consolidation is not available: there is no verified way to run Codex
  headless with tools disabled.

## Gemini CLI

- Settings: `~/.gemini/settings.json`. Hooks are entries with `matcher: "*"`.
- Hook events installed: SessionStart, BeforeAgent, AfterTool, AfterAgent,
  SessionEnd.
- Written from the Gemini CLI documentation (geminicli.com, 2026-11-10). Not
  verified against a live Gemini CLI. Field names the docs leave open are read
  defensively.
- Hive cannot block a tool call on Gemini (no pre-tool hook).
- If `hooksConfig.enabled` is `false` in the settings file, Gemini does not run
  any hooks. `hive doctor` reports this.
- Consolidation is not available: no verified headless mode with tools disabled.

## Hookless agents

An agent without usable hooks can still get memory through `hive sync`, which
writes the project's memories into the agent's instructions file (`CLAUDE.md`,
`AGENTS.md`, `GEMINI.md`) between `<!-- hive:begin -->` and `<!-- hive:end -->`.
Only this project's memories are written unless `--include-global` is given.
The file may be committed, so review the block first.

## MCP

`hive mcp` serves `remember`, `recall`, `forget` and `why` over stdio. `forget`
marks a memory forgotten and never purges it; purge is only on the CLI.
