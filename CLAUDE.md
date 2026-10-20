# docs-rag-ingest

## Feature ledger

`FEATURES.md` at the repo root is the living ledger of subsystems (entry points, data dependencies, trade-offs). Read it before changing an unfamiliar area.

After any change with behavioral or architectural impact (new feature, schema change, refactor, removal), run `/ledger-update` before finishing so the ledger stays current.

## Custom slash commands

- `/onboard [focus area]` (`.claude/commands/onboard.md`): architecture and data-flow discovery, system design interview prep, and creates `FEATURES.md` if missing.
- `/ledger-update [feature or change summary]` (`.claude/commands/ledger-update.md`): syncs `FEATURES.md` with the current git changes.
