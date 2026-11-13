## Learned User Preferences

- Keep extractors and prompt-time recall model-free. Do not add an LLM on the hook path; LLM review was deferred there.
- Normalize every agent's transcript into one shared Letta trajectory record, and keep learned memories in Inscribe's typed schema rather than storing them as trajectory turns.

## Learned Workspace Facts

- Inscribe is a local-first Go memory engine for coding agents. Hooks capture Claude Code, Codex, and Gemini CLI; plain Go extractors upsert typed memories in SQLite; recall injects a few short lines at session start, on prompts, and when a known error returns. Capture and recall make no network or model call.
- `inscribe hook --agent <name> <event>` reads hook JSON on stdin. Adapters turn that into one Event, secrets are scrubbed, and `trajectory.Normalize` writes Letta trajectory v1 rows to `trajectory_records` in the same transaction. Extractors upsert memories from events; they do not emit trajectory records.
- Memories use Inscribe fields (type, scope, key, confidence, status, evidence). Recall injects only `active` project memories plus global ones. Candidates, forgotten, stale, and superseded rows stay stored and are not injected.
- Prompt ranking stays local. The text grade is the floor; key, body, type, scope, agent, command, and the captured trajectory JSON add `score_model` weight only when they match the prompt better than the text. Confidence, a 30-day recency half-life, and scope then scale it. Prompts past 12 content words are scored in short windows, and each memory keeps its best window. Lines under the prompt threshold (default 0.35) are dropped. Session start and error lookup do not use that tree.
- Session start and session end spawn a detached `inscribe score train`, which labels finished-session injections and updates the eight local `score_model` weights.
- Session-end consolidation is on by default; `consolidate.auto = false` in `~/.inscribe/config.toml` turns it off. When enabled it spawns a detached `claude -p` review with `INSCRIBE_DISABLED=1` and skips sessions with fewer than 20 new events.
