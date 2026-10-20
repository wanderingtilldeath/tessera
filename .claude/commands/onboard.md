---
description: Onboard to an unfamiliar codebase - architecture, schema, request tracing, system design interview prep, and FEATURES.md setup
argument-hint: "[optional focus area, e.g. auth, payments, database]"
---

Analyze this repository using the universal codebase onboarding and architecture discovery standard.

### Parameters
- Target: current workspace directory
- Focus Area: $ARGUMENTS (if provided, spend Phases 2 and 3 on this module or flow; keep Phase 1 brief)

### Ground rules
- **Read-only**, except creating `FEATURES.md` in Phase 4. Do not start services, install dependencies, run migrations, or commit.
- **Never read or echo secrets**: skip real `.env` files, keys, and credentials. `.env.example` is fine.
- **Cite real paths.** Every claim names the file it came from (`path/file.ext`, with a line number where useful). If something is not in the code, write "not found" instead of guessing.
- **Docs are leads, not proof.** Read the README, `docs/`, ADRs and any `GLOSSARY.md` first, then confirm the claims against the code and say where they disagree.
- Do not read every file. On a large repo, delegate broad searching to the Explore agent and keep only conclusions.

---

### Execution Protocol

#### Phase 1: Blueprint & Stack
1. **Language & Runtime**: identify the stack from manifests (`package.json`, `go.mod`, `Cargo.toml`, `pyproject.toml`, `pom.xml`, `Gemfile`, etc.), plus build tooling (Makefile, scripts).
2. **Entry Points**: locate application bootstrap and input handlers (HTTP server bootstrap, CLI runner, workers, cron).
3. **Environment & Infrastructure**: review `.env.example`, Dockerfiles, docker-compose, orchestration and CI config. List the external services the app needs to run.

#### Phase 2: Architecture & Data Model (How Data Flows)
1. **Topology Style**: monolith, modular monolith, microservices, or event-driven worker/pub-sub pipeline. Say which and why.
2. **Data & Schema**:
   - Locate database definitions (SQL migrations, ORM schemas, NoSQL models).
   - Summarize core entities, relationships, keys, constraints and indexes, noting the reason for each non-obvious index.
3. **Trace One Core Path**: follow one representative end-to-end request and name the real file and function at each hop:
   `Router/Handler` → `Auth/Validation` → `Business Logic/Service` → `Storage/Cache` → `Response`
   If a hop does not exist (e.g. no auth), say so explicitly.

#### Phase 3: Production Patterns & Interview Prep
Explain findings in plain, language-neutral technical terms:
- **Caching**: what is cached (CDN, Redis, in-memory), with what TTL, and how it is invalidated.
- **Statelessness & Scaling**: can multiple instances run behind a load balancer? Where does shared state live? What coordinates concurrent workers (locks, leases, queues)?
- **Resilience**: retries, backoff, circuit breakers, rate limiters, timeouts, fallbacks, dead-letter handling, crash recovery.
- **Security posture**: authentication, input limits, SSRF or injection surfaces, and anything left unprotected on purpose.
- **System Design Interview Prep**:
  - State the classic interview question this system represents (e.g. *"Design an idempotency-backed payment engine"*).
  - Analyze real trade-offs: what was prioritized (consistency vs. availability, simplicity vs. extensibility) and what breaks first at 10x traffic.
  - Provide 3 targeted interview questions, each tied to a real pattern or file in this repo, with a short model answer.

#### Phase 4: Bootstrap FEATURES.md
1. Get the entry stamp: `date +%F` and `git rev-parse --short HEAD` (omit the hash outside git).
2. Check if `FEATURES.md` exists at the project root.
   - **Missing**: create it with the heading `# Features` and seed 5 to 15 core active subsystems using the template below.
   - **Present**: do not overwrite it. Report subsystems it is missing and suggest `/ledger-update`.
3. Verify every path listed in an entry exists before writing it.

```markdown
### [Feature / Subsystem Name]
- **Status**: Active
- **Last Updated**: YYYY-MM-DD @ <short commit hash>
- **Entry Points / Key Files**:
  - `path/to/entry.ext`: route / handler
  - `path/to/service.ext`: business logic
  - `path/to/schema.ext`: data definitions
- **Implementation Summary**: Plain-language description of how it works internally (2 to 5 sentences).
- **Data & Schema Dependencies**: Tables, collections, external APIs, queues, or caches touched.
- **Production Trade-Offs**: Known edge cases, locking/idempotency rules, caching behavior, or scaling limits.
```

---

### Final report
Answer in chat with these headings, in order: **Stack**, **Topology**, **Data model**, **Core request path**, **Production patterns**, **Interview prep**, **FEATURES.md** (created or gaps found), **Open questions** (things the code could not answer).
