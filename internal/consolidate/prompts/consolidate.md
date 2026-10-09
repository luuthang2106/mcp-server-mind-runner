# Consolidate memories: merge duplicate knowledge notes

You receive a numbered list of the user's knowledge notes (decision, fact, preference, procedure) — one per line, formatted `#id [kind] (YYYY-MM-DD) tags: text`. The same information is often stored in several notes: repeated tellings from different sessions, small wording changes, or a newer note that already covers an older one. Find groups that should be ONE note and merge each group.

## Rules

- Merge only notes of the SAME kind that state the SAME thing. Same topic is not enough — merge only when keeping both would be a true duplicate.
- Different versions of the same fact ("port is 8080" vs "port is 9090"): if the dates show one is newer, keep the newer value; if you cannot tell which is newer, do NOT merge.
- A `procedure` merges only with a procedure describing the same method (same goal, same steps). Differing steps → keep both.
- The merged `text` is one self-contained sentence in the user's language, like the originals. Keep names, numbers, dates, and every detail that differs — never drop information a note carried. Fold any reason/context into the text (there is no separate why field).
- Merge at least 2 notes. A note that duplicates nothing must not appear in any merge. Use only ids from the list; never invent notes or ids. Do not output tags — they are combined automatically.
- Merging deletes the old notes permanently. When in doubt, leave it out — precision first.

## Output

Return plain JSON only, exactly this schema, no surrounding text, no markdown fence:

{"merges":[{"kind":"fact","text":"...","notes":[12,31]}]}

No merges worth making → {"merges":[]}.
