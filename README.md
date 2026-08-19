# Mergebase

Version control for database schemas — branch a schema, evolve branches independently,
see exactly what diverged, and merge back safely with conflict resolution, whole-schema
validation, and an ordered migration script.

> Status: under active build (project round, day 1). This README grows with the code;
> the design and every real decision live in [decisions.md](decisions.md).

## What it does

- **Import** a PostgreSQL schema (paste DDL) → it becomes a versioned snapshot.
- **Branch** it and evolve each branch independently: add / drop / **rename** / retype
  columns, change constraints and indexes, create / drop tables.
- **Diff** any two branches or commits — semantically ("column `email` renamed and
  retyped"), not as text lines.
- **Merge** back with a true three-way merge: non-overlapping changes combine
  automatically, real collisions stop the merge and are resolved explicitly
  (ours / theirs / provide-a-value).
- **Validate**: a merge only lands if the combined schema is coherent — no dangling
  foreign keys, no orphaned indexes, no duplicate names.
- **Migrate**: get an ordered SQL script that carries one version to the other.
  Generated, never executed.

Row data is out of scope by design — the schema itself is the versioned artifact.

## Run it

```sh
go run ./cmd/server            # then open http://localhost:8080
```

or

```sh
docker compose up
```

Configuration: `PORT` (default 8080) and `DATABASE_PATH` (default `./data/mergebase.db`).
First run with an empty database seeds a demo workspace.

## Tests

```sh
go test ./...
```

The engine packages (`internal/schema`, `parser`, `diff`, `merge`, `validate`,
`migrate`) are pure Go — no HTTP, no storage — and carry the bulk of the test suite,
including a conflict-taxonomy suite and the system's seven invariants as property tests.
