# Decisions

A running log of the real calls made while building Mergebase — kept as I went, not
reconstructed at the end. Newest entries at the bottom. Each entry: what I chose, what
else I seriously considered, why, and what I deliberately cut.

## The brief

**The problem.** Version control for database schemas: branch a schema, evolve branches
independently — add, drop, rename, and retype columns; change constraints and indexes;
create and drop tables — then see exactly what diverged and merge back. Row data is out
of scope: the schema itself is the versioned artifact.

**The user.** An engineering team where several people evolve one database schema in
parallel, and today reconcile changes by hand, by migration-file archaeology, or by
shouting across the room before touching a table.

**The hard part.** Merging — specifically three things most implementations quietly skip:
(1) telling a *rename* apart from a *drop + add*, which text comparison cannot do and
which, read wrongly, destroys data on a real database; (2) conflicts that exist only
*across* objects — two individually valid branches whose combination is broken, like an
FK pointing at a table the other branch dropped; (3) emitting a migration script whose
statement order actually runs, including in the presence of circular foreign keys.

**The slice.** One end-to-end path, shipped working: import DDL → commit → branch →
edit → semantic diff → three-way merge with conflict resolution → validated merge
commit → ordered migration script. PostgreSQL dialect only, one shared workspace, no auth.

---

## Decision log

### 1. This problem, over document extraction — 2026-08-18

- **Decision:** build Problem 2 (schema version control), not Problem 1 (documents →
  structured data).
- **Alternatives:** Problem 1 framed around an extraction trust layer (confidence
  scoring, validation, human review of LLM output).
- **Reasoning:** judged strictly against the evaluation criteria, Problem 2 wins the
  rows that separate submissions. *Tests:* a merge engine is deterministic, so tests can
  prove correctness rather than fence a probabilistic model. *Setup:* no API keys, no
  rate limits, nothing external that can flake while a reviewer clicks. *Depth:* the
  hard sub-problems (rename identity, cross-object conflicts) are unmistakably my code,
  not a model's output.
- **Cut / trade-off accepted:** Problem 1 demos flashier and aligns with fintech
  document processing; I accepted a quieter demo whose depth has to earn attention
  through the merge flow itself.

### 2. Version the semantic model, not SQL text — 2026-08-18

- **Decision:** parse DDL into a structured schema model; diff, merge, and validation
  operate only on the model. SQL is the input and output format, never the compared thing.
- **Alternatives:** version `.sql` files and diff them textually — effectively what
  storing migrations in git already gives people.
- **Reasoning:** text can never distinguish `RENAME email → email_address` from
  `DROP email; ADD email_address`. On a real database one preserves the column's data,
  the other destroys it. That single distinction is the product's reason to exist, and
  it requires structure.
- **Cut:** SQL formatting/comment preservation on round-trip — exports are regenerated,
  not spliced.

### 3. Stable object identity, and references stored by ID — 2026-08-18 (revised 2026-08-19)

- **Decision:** every table and column carries a stable internal ID that survives
  renames. Foreign keys, constraints, and indexes reference tables/columns *by ID*,
  never by name; names are resolved only at SQL-emit and display time.
- **Alternatives:** name-keyed model (simpler, and what a naive implementation does);
  IDs on objects but name-based references (my own first draft).
- **Reasoning:** identity is what makes rename-aware diff and merge exact instead of
  guessed. The revision came from an external review pass on the design: name-based
  references collapse exactly where identity matters most — ours renames
  `users → accounts`, theirs adds an FK to `users`, and by name that manufactures a
  dangling-FK conflict out of two changes that compose cleanly. By ID the FK follows
  the rename. Fixing this on day 1 was a decision; on day 4 it would have been a refactor.
- **Cut:** nothing — this made the design smaller, not larger.

### 4. PostgreSQL subset, documented, single dialect — 2026-08-18

- **Decision:** support one dialect (PostgreSQL) with an explicitly documented subset
  of DDL.
- **Alternatives:** MySQL + Postgres + SQLite support; or dialect-agnostic "generic SQL".
- **Reasoning:** one well-supported dialect demonstrates every hard problem in the
  assignment; three shallow dialects demonstrate none of them well. A documented subset
  is a feature — the user knows exactly what the tool understands.
- **Cut:** views, triggers, functions, sequences, partitions, schemas/namespaces. The
  assignment's own vocabulary is tables/columns/constraints/indexes; I matched it.

### 5. Reuse a real parser instead of writing one — 2026-08-18

- **Decision:** use `pg_query_go` (the actual PostgreSQL grammar as a library) to parse
  DDL, and map its tree onto my model.
- **Alternatives:** hand-rolled parser for a strict DDL subset.
- **Reasoning:** the interesting problem here is diff/merge semantics, not
  re-implementing a 30-year-old grammar. A hand-rolled parser is the classic place a
  project like this silently loses two days to quoting and expression edge cases.
- **Cut / trade-off accepted:** CGO dependency (debian-slim Docker base instead of
  alpine). Fallback decided in advance: if integration fights back more than half a day,
  drop to a hand-rolled parser for `CREATE TABLE` / `CREATE INDEX` only.

### 6. Whole snapshots per commit, not deltas — 2026-08-18

- **Decision:** each commit stores the complete schema model as JSON.
- **Alternatives:** store operation deltas and reconstruct; store both.
- **Reasoning:** schemas are kilobytes. Whole snapshots make checkout free, diff
  trivial to reason about, and debugging honest (`what did commit 17 look like` is a
  SELECT, not a replay). Delta storage is a real design — for a problem this data size
  does not have.
- **Cut:** storage efficiency I measurably do not need.

### 7. Three-way merge, gated by whole-schema validation — 2026-08-18

- **Decision:** merge uses the common ancestor (merge-base) and compares per *property*;
  a merge produces a commit only if the merged schema passes a full validation pass
  (every FK resolves, constraints/indexes reference live columns, no duplicate names).
- **Alternatives:** two-way diff-and-pick (no ancestor); merge without a validation gate.
- **Reasoning:** without the ancestor you cannot tell "only one side changed this —
  apply it" from "both changed it — ask". And a conflict-free merge can still be broken:
  each branch valid alone, the combination invalid (FK to a table the other side
  dropped). Validation is what makes "no branch head ever points at a broken schema" a
  guarantee instead of a hope.
- **Cut:** rebase and cherry-pick — merge alone demonstrates the hard part.

### 8. Phased migration emission, not a topological sort — 2026-08-19

- **Decision:** emit migration DDL in a fixed phase order (drop indexes → drop FKs →
  drop constraints → drop columns → drop tables → renames → create tables *without*
  FKs → alter columns → add non-FK constraints → add FKs → create indexes).
- **Alternatives:** dependency-graph topological sort with inline FK emission (my first
  draft, corrected by review).
- **Reasoning:** a topo sort with inline FKs is broken by construction on circular
  foreign keys — two tables referencing each other cannot both be created first, in any
  order. The phase order is cycle-proof, deterministic, and needs no graph algorithm at
  all. It made the planned implementation smaller.
- **Cut / bounded claim:** the round-trip guarantee is *schema-level*. A structurally
  correct script can still fail against real data (`ALTER TYPE` without `USING`,
  `SET NOT NULL` over nulls, `ADD COLUMN NOT NULL` without default) — the generator
  emits explicit warnings for exactly those three cases instead of pretending otherwise.

### 9. Three resolution kinds, not two — 2026-08-19

- **Decision:** a conflict is resolved as *ours*, *theirs*, or *provide-a-value* —
  first-class in the conflict schema, the API payload, and the UI.
- **Alternatives:** binary ours/theirs (simpler UI, my first draft).
- **Reasoning:** some conflicts are unwinnable in a binary model: ours renames table
  `a → c`, theirs renames `b → c` — each rename individually clean, but the merged
  schema would hold two tables named `c`, and neither side's answer fixes it. The user
  must supply a new name. Retrofitting a third kind later touches three layers;
  building it in touches one.
- **Cut:** free-form "edit the merged schema arbitrarily during resolution" — resolution
  answers the conflict, it is not a second editor.

### 10. Unsupported DDL is recorded, never silently dropped — 2026-08-19

- **Decision:** parse everything, model the documented subset, and store an explicit
  per-commit list of constructs that were recognized but not modeled. Import warns;
  export states what it does not cover.
- **Alternatives:** silently skip unknown statements (what most quick implementations do).
- **Reasoning:** a user who imports and then exports must never get back *less than they
  put in with no indication*. This converts the project's biggest quiet fidelity risk —
  the parser-to-model mapping — into a documented boundary. It ranks above merge-engine
  risk precisely because its failure mode is silent.
- **Cut:** actually modeling those constructs (see decision 4).

### 11. Go + SQLite (pure-Go driver) + embedded React, one container — 2026-08-18

- **Decision:** Go backend; SQLite via `modernc.org/sqlite` for application state
  (one file, `DATABASE_PATH`); React/TypeScript frontend embedded into the binary via
  `go:embed`; one Docker container; config is `PORT` + `DATABASE_PATH`, nothing else.
- **Alternatives:** Postgres for app storage; separate frontend hosting; HTMX instead
  of React.
- **Reasoning:** Postgres would add an external service that can be down while a
  reviewer clicks, for query patterns (fetch snapshot by ID, list commits) that a single
  file serves perfectly — for a 5-day build, one datastore, zero provisioning. The
  diff/merge/conflict screens are state-heavy interactive UI, which is React's home
  ground. One container means the deployed thing and the local thing are the same thing.
- **Cut:** auth and multi-tenancy — one shared workspace. Concurrent writers are still
  handled where it matters: branch-head moves are compare-and-swap inside the commit
  transaction, so two simultaneous merges cannot silently discard each other.

### 12. Migrations are generated, never executed — 2026-08-18

- **Decision:** the migration script is produced to view, copy, or download. Mergebase
  never connects to a user's database and never runs DDL.
- **Alternatives:** "apply" button executing against a connection string.
- **Reasoning:** execution drags in credentials, transactions, rollback, and production
  safety — a different product, none of it needed to demonstrate this one. The boundary
  is also the safety story: this tool cannot break your database, by construction.
- **Cut:** live-database introspection as an import source (paste DDL instead).
