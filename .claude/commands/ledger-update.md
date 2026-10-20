---
description: Sync FEATURES.md with recent code changes, new features, schema updates, or refactors
argument-hint: "[optional feature name or change summary]"
---

Inspect recent repository changes and update the `FEATURES.md` ledger.

### Parameters
- Hint / Feature Name: $ARGUMENTS (if provided, match it against existing entries first and treat it as the change summary)

### Ground rules
- Edit only `FEATURES.md`. Do not commit, and do not touch entries unaffected by the changes.
- Verify before writing: every path in an entry must exist, and summaries must match the code as it is now, not the commit message.

---

### Protocol

1. **Detect Changes**
   - Run `git status --short`, `git diff` and `git diff --staged`.
   - If the working tree is clean (or the hint refers to committed work), use history instead: take the newest hash in any `Last Updated` field of `FEATURES.md` as the baseline and run `git log --name-status <hash>..HEAD`. If there is no usable hash, use the last 10 commits.
   - Outside git, check files modified during the current session.
   - Ignore changes with no behavioral or architectural impact: formatting, comments, typo fixes, and test-only edits (unless they reveal a behavior change).

2. **Open `FEATURES.md`**
   - Look in the project root. If it does not exist, create it with the heading `# Features`, and suggest running `/onboard` for a full seed.

3. **Match & Update**
   - Match each changed file to an entry by its listed Key Files, then by directory. Read the changed code, not just the diff header.
   - **Existing subsystem changed**: update the entry in place, set `Status: Modified`, refresh file paths, summary, dependencies and trade-offs, and bump `Last Updated`.
   - **New capability**: append a new entry at the bottom with `Status: Active`.
   - **Code removed or deprecated**: set `Status: Deprecated` and name what replaces it. Never delete history.
   - **Stamp**: `Last Updated: YYYY-MM-DD @ <short hash>` from `date +%F` and `git rev-parse --short HEAD`. Append `+uncommitted` if the change is not committed yet.

4. **Entry Template**

```markdown
### [Feature / Subsystem Name]
- **Status**: Active | Modified | Deprecated
- **Last Updated**: YYYY-MM-DD @ <short commit hash>
- **Entry Points / Key Files**:
  - `path/to/entry.ext`: route / handler
  - `path/to/service.ext`: business logic
  - `path/to/schema.ext`: data definitions
- **Implementation Summary**: Plain-language description of how it works internally (2 to 5 sentences).
- **Data & Schema Dependencies**: Tables, collections, external APIs, queues, or caches touched.
- **Production Trade-Offs**: Edge cases, locking/idempotency behavior, caching rules, scaling limits, or known gaps.
```

5. **Report**: list entries added, modified and deprecated (one line each, with the files that triggered it), and anything you skipped as non-behavioral.
