# Agents

Inscribe hooks into three coding agents. Each has its own settings file and its own
hook payloads. This page records what Inscribe relies on and how it was checked.

## Claude Code

- Settings: `~/.claude/settings.json`. Verified live against Claude Code.
- Hook events: SessionStart, UserPromptSubmit, PostToolUse, PostToolUseFailure,
  Stop, SessionEnd. Inscribe installs no PreToolUse hook.
- Consolidation runs here only: `claude -p` with tools disabled.

## Codex

- Settings: `~/.codex/hooks.json`, with events under a top-level `hooks` object.
  Verified on Codex 0.163.0 (2026-11-10) and the Codex hook documentation.
- Hook events installed: SessionStart, UserPromptSubmit, PostToolUse, Stop,
  SessionEnd. Inscribe installs no PreToolUse hook: it never blocks a tool call.
- After `inscribe install --agent codex`, open `/hooks` in Codex and approve the
  Inscribe hooks. Codex does not run a new or changed hook until it is approved.
- Consolidation is not available: there is no verified way to run Codex
  headless with tools disabled.

## Gemini CLI

- Settings: `~/.gemini/settings.json`. Hooks are entries with `matcher: "*"`.
- Hook events installed: SessionStart, BeforeAgent, AfterTool, AfterAgent,
  SessionEnd.
- Written from the Gemini CLI documentation (geminicli.com, 2026-11-10). Not
  verified against a live Gemini CLI. Field names the docs leave open are read
  defensively.
- Inscribe cannot block a tool call on Gemini (no pre-tool hook).
- If `hooksConfig.enabled` is `false` in the settings file, Gemini does not run
  any hooks. `inscribe doctor` reports this.
- Consolidation is not available: no verified headless mode with tools disabled.

## Hookless agents

An agent without usable hooks can still get memory through `inscribe sync`, which
writes the project's memories into the agent's instructions file (`CLAUDE.md`,
`AGENTS.md`, `GEMINI.md`) between `<!-- inscribe:begin -->` and `<!-- inscribe:end -->`.
Only this project's memories are written unless `--include-global` is given.
The file may be committed, so review the block first.

## MCP

`inscribe mcp` serves `remember`, `recall`, `forget` and `why` over stdio. `forget`
marks a memory forgotten and never purges it; purge is only on the CLI.
