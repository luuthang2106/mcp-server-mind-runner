# Extract memories from a work session

You receive part of a Claude Code session transcript, condensed to lines prefixed `User:` (the person), `Assistant:` (the AI) and `Tool:` (an action the AI took). It may start with the user's current open tasks. Extract what is worth remembering for future sessions. Write every `text`, `why` and `title` in the language the user speaks in the transcript.

## Precision first

A wrong or duplicate memory is worse than a missing one. When unsure, leave it out.

- Extract only what the user states, asks for, or explicitly accepts ("ok", "làm đi", "đúng rồi" right after a proposal counts as accepting that proposal).
- The assistant's suggestions, plans, explanations and claims are NOT memories unless the user accepted them. Tool lines are context only.
- Prefer few, high-value items. A short session often has nothing worth remembering — return empty arrays.

## Kinds

- `decision` — a choice that was settled. Put the reason in `why` and rejected options in `alternatives` when the transcript states them.
- `fact` — a stable truth about the user's projects, systems, people or setup. Use `as_of` (YYYY-MM-DD) when it can change, `ref` for its source (file, URL, ticket).
- `preference` — how the user likes things done (answer style, tools, conventions). `scope` when it applies only somewhere (e.g. "Go code", "commit messages").
- `procedure` — a repeatable way of doing something the user relies on ("how we deploy", "how I triage tickets"): the method, ordered steps or rules. Only when the transcript states the method; not a one-off event.
- `note` — something that happened (what was done or discussed), not knowledge. `who`/`when` when stated.
- `task_hint` — an unfinished thing mentioned in passing that is not concrete enough to be a task.

Each `text` is one self-contained sentence with enough context to be understood alone — no vague pronouns ("it", "that thing"). Skip small talk, repeats, transient details (one-off errors, intermediate attempts), things obvious from the code, and secret values (keys, passwords, tokens). Account facts ARE worth extracting: which account/username is used for which service, and where the credential lives (Keychain item, vault, env).

## Never invent

Fill an optional field only when the transcript says it. Unknown → omit the field or use "" / []. Dates only as YYYY-MM-DD, and only when the exact date is clear.

## Tasks

Add to `tasks` only concrete open work the user (or someone) still has to do after this session. NOT tasks: the assistant's own in-session steps (answering a question, investigating, writing a report, waiting for a subagent, running tests) and anything finished within the transcript. Optional: `next_step` (the concrete next action), `why`, `owner` (who does it, if not the user), `waiting_on` (who/what it is blocked on), `due` (YYYY-MM-DD), `constraints`.

If the work is already in the open tasks list (same goal, even if worded differently), do NOT add it to `tasks` — use `task_updates` with its id instead:
- `status`: "done" only when the transcript clearly shows it was finished; "dropped" only when the user abandons it; otherwise "".
- `next_step` / `waiting_on` / `due`: only when the transcript changes them.
Use only ids from the open tasks list. Nothing changed for a task → no entry.

## Relations

Add `relations` only when two entities are explicitly related in the transcript (e.g. a task depends on a decision). `note_index` is the 0-based index of the note in `notes` the relation belongs to; omit it if none.

## Output

Return plain JSON only, exactly this schema, no surrounding text, no markdown fence:

{"notes":[{"kind":"decision","text":"...","tags":["..."],"why":"...","alternatives":["..."],"who":["..."],"when":"YYYY-MM-DD","as_of":"YYYY-MM-DD","ref":"...","scope":"..."}],"tasks":[{"title":"...","next_step":"...","why":"...","owner":"...","waiting_on":"...","due":"YYYY-MM-DD","constraints":"..."}],"task_updates":[{"id":12,"status":"done","next_step":"...","waiting_on":"...","due":"YYYY-MM-DD"}],"relations":[{"from":"...","to":"...","type":"...","note_index":0}]}

Nothing to extract → empty arrays.
