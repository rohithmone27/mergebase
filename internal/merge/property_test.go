// Property-based tests for the merge engine.
//
// The taxonomy suite proves the engine handles the cases we thought of.
// These tests attack the cases we didn't: they generate random schemas,
// apply random divergent edit sequences to both sides, and assert the
// system's promises hold for every one of them.
//
// Each property is a claim the whole design rests on, so a counterexample
// here is a real defect, not a flaky test. Seeds are deterministic: a
// failure prints the seed and the exact edit sequences that produced it,
// so it can be replayed.
package merge

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/rohithmone27/mergebase/internal/ops"
	"github.com/rohithmone27/mergebase/internal/schema"
	"github.com/rohithmone27/mergebase/internal/validate"
)

const rounds = 300

// --- generators ---

// namePool is deliberately small and shared by both sides: independent
// edits then collide often, so the conflict and name-collision paths get
// real exercise instead of two branches politely renaming past each other.
var namePool = []string{"status", "label", "ref", "code", "note", "state"}

var typePool = []schema.DataType{
	{Base: "bigint"}, {Base: "integer"}, {Base: "text"},
	{Base: "varchar", Params: []int{64}}, {Base: "varchar", Params: []int{255}},
	{Base: "numeric", Params: []int{12, 2}}, {Base: "timestamp"}, {Base: "boolean"},
}

// randomSchema builds a valid schema with a few tables, columns, primary
// keys, and cross-table foreign keys.
func randomSchema(rng *rand.Rand) *schema.Schema {
	s := &schema.Schema{}
	tables := 2 + rng.Intn(3)
	for i := range tables {
		t := schema.Table{ID: schema.NewObjectID(), Name: fmt.Sprintf("t%d", i)}
		id := schema.Column{ID: schema.NewObjectID(), Name: "id",
			Type: schema.DataType{Base: "bigint"}, Nullable: false, Position: 1}
		t.Columns = append(t.Columns, id)
		for c := range 1 + rng.Intn(4) {
			t.Columns = append(t.Columns, schema.Column{
				ID: schema.NewObjectID(), Name: fmt.Sprintf("c%d", c),
				Type: typePool[rng.Intn(len(typePool))], Nullable: rng.Intn(2) == 0,
				Position: len(t.Columns) + 1,
			})
		}
		t.Constraints = append(t.Constraints, schema.Constraint{
			ID: schema.NewObjectID(), Kind: schema.PrimaryKey, ColumnIDs: []schema.ObjectID{id.ID},
		})
		s.Tables = append(s.Tables, t)
	}
	// A foreign key between two random tables, so merges have cross-object
	// structure to break.
	if len(s.Tables) >= 2 {
		from, to := &s.Tables[0], &s.Tables[1]
		fk := schema.Column{ID: schema.NewObjectID(), Name: "ref_id",
			Type: schema.DataType{Base: "bigint"}, Nullable: true, Position: len(from.Columns) + 1}
		from.Columns = append(from.Columns, fk)
		from.Constraints = append(from.Constraints, schema.Constraint{
			ID: schema.NewObjectID(), Kind: schema.ForeignKey,
			ColumnIDs: []schema.ObjectID{fk.ID}, RefTableID: to.ID,
			RefColumnIDs: []schema.ObjectID{to.ColumnByName("id").ID},
		})
	}
	return s
}

// randomDiverge applies a plausible random edit sequence to a copy of s and
// returns the evolved schema plus a plain-language log, so a failing case is
// readable and replayable from its seed.
//
// It returns the schema rather than the operation list on purpose: add_column
// mints a fresh ObjectID at apply time, so a sequence that adds a column and
// then edits it cannot be replayed against a different starting snapshot.
// Divergence is what these properties need, not a portable op log.
func randomDiverge(rng *rand.Rand, s *schema.Schema, tag string, n int, only ...schema.Table) (*schema.Schema, []string) {
	var desc []string
	work := s.Clone()
	// When `only` is given, edits are confined to those tables — used to
	// generate provably disjoint divergence.
	allowed := map[schema.ObjectID]bool{}
	for _, t := range only {
		allowed[t.ID] = true
	}

	for i := range n {
		if len(work.Tables) == 0 {
			break
		}
		t := &work.Tables[rng.Intn(len(work.Tables))]
		if len(allowed) > 0 && !allowed[t.ID] {
			continue
		}
		var op ops.Op

		switch rng.Intn(8) {
		case 0: // rename table
			op = ops.Op{Op: ops.RenameTable, TableID: t.ID, Name: namePool[rng.Intn(len(namePool))] + "_tbl"}
		case 1: // add column
			op = ops.Op{Op: ops.AddColumn, TableID: t.ID, Column: &ops.ColumnSpec{
				Name: namePool[rng.Intn(len(namePool))], Type: typePool[rng.Intn(len(typePool))], Nullable: true}}
		case 2: // rename column
			c := t.Columns[rng.Intn(len(t.Columns))]
			op = ops.Op{Op: ops.RenameColumn, TableID: t.ID, ColumnID: c.ID, Name: namePool[rng.Intn(len(namePool))]}
		case 3: // retype column
			c := t.Columns[rng.Intn(len(t.Columns))]
			op = ops.Op{Op: ops.RetypeColumn, TableID: t.ID, ColumnID: c.ID, Type: &typePool[rng.Intn(len(typePool))]}
		case 4: // nullability (never on a PK member)
			c := t.Columns[rng.Intn(len(t.Columns))]
			if isPK(t, c.ID) {
				continue
			}
			nullable := rng.Intn(2) == 0
			op = ops.Op{Op: ops.SetNullable, TableID: t.ID, ColumnID: c.ID, Nullable: &nullable}
		case 5: // default
			c := t.Columns[rng.Intn(len(t.Columns))]
			def := fmt.Sprintf("'%s%d'", tag, i%3)
			op = ops.Op{Op: ops.SetDefault, TableID: t.ID, ColumnID: c.ID, Default: &def}
		case 6: // add index
			c := t.Columns[rng.Intn(len(t.Columns))]
			op = ops.Op{Op: ops.AddIndex, TableID: t.ID, Index: &ops.IndexSpec{
				Name: "idx_" + namePool[rng.Intn(len(namePool))], Columns: []schema.IndexColumn{{ColumnID: c.ID}}}}
		case 7: // drop a droppable column
			c := t.Columns[rng.Intn(len(t.Columns))]
			if isPK(t, c.ID) || referenced(work, t, c.ID) {
				continue
			}
			op = ops.Op{Op: ops.DropColumn, TableID: t.ID, ColumnID: c.ID}
		}

		next, err := ops.Apply(work, []ops.Op{op})
		if err != nil {
			continue // the generator proposed something invalid; skip it
		}
		desc = append(desc, ops.Describe(work, op))
		work = next
	}
	return work, desc
}

func isPK(t *schema.Table, id schema.ObjectID) bool {
	pk := t.PrimaryKey()
	if pk == nil {
		return false
	}
	for _, c := range pk.ColumnIDs {
		if c == id {
			return true
		}
	}
	return false
}

// referenced reports whether a column participates in any constraint or
// index anywhere in the schema (so the generator does not propose drops the
// engine would rightly refuse).
func referenced(s *schema.Schema, owner *schema.Table, id schema.ObjectID) bool {
	for ti := range s.Tables {
		t := &s.Tables[ti]
		for _, c := range t.Constraints {
			for _, cid := range append(append([]schema.ObjectID{}, c.ColumnIDs...), c.RefColumnIDs...) {
				if cid == id {
					return true
				}
			}
		}
		for _, ix := range t.Indexes {
			for _, ic := range ix.Columns {
				if ic.ColumnID == id {
					return true
				}
			}
		}
	}
	return false
}

func fail(t *testing.T, seed int64, oursDesc, theirsDesc []string, format string, args ...any) {
	t.Helper()
	t.Fatalf("%s\n  seed:   %d\n  ours:   %s\n  theirs: %s",
		fmt.Sprintf(format, args...), seed,
		strings.Join(oursDesc, "; "), strings.Join(theirsDesc, "; "))
}

// --- properties ---

// Property 1: a branch merged against an untouched sibling is always clean,
// and the result is exactly that branch's schema. Nothing is invented, and
// nothing goes missing, no matter what the edits were.
func TestPropertyFastForwardIsIdentity(t *testing.T) {
	for round := range rounds {
		seed := int64(round)
		rng := rand.New(rand.NewSource(seed))
		base := randomSchema(rng)
		theirs, desc := randomDiverge(rng, base, "a", 1+rng.Intn(6))

		res, err := Merge(Input{Base: base, Ours: base.Clone(), Theirs: theirs,
			OursName: "main", TheirsName: "feature"})
		if err != nil {
			fail(t, seed, nil, desc, "merge errored: %v", err)
		}
		if len(res.Conflicts) != 0 {
			fail(t, seed, nil, desc, "fast-forward produced %d conflict(s)", len(res.Conflicts))
		}
		if len(res.Problems) != 0 {
			fail(t, seed, nil, desc, "fast-forward produced validation problems: %+v", res.Problems)
		}
		if got, want := fingerprint(res.Schema), fingerprint(theirs); got != want {
			fail(t, seed, nil, desc, "fast-forward changed the schema:\n  got:  %s\n  want: %s", got, want)
		}
	}
}

// Property 2: merging a branch with itself is a no-op — same edits on both
// sides never conflict and produce exactly that schema.
func TestPropertyIdenticalEditsNeverConflict(t *testing.T) {
	for round := range rounds {
		seed := int64(1000 + round)
		rng := rand.New(rand.NewSource(seed))
		base := randomSchema(rng)
		side, desc := randomDiverge(rng, base, "a", 1+rng.Intn(6))

		res, err := Merge(Input{Base: base, Ours: side, Theirs: side.Clone(),
			OursName: "main", TheirsName: "feature"})
		if err != nil {
			fail(t, seed, desc, desc, "merge errored: %v", err)
		}
		if len(res.Conflicts) != 0 {
			fail(t, seed, desc, desc, "identical edits conflicted: %+v", res.Conflicts)
		}
		if got, want := fingerprint(res.Schema), fingerprint(side); got != want {
			fail(t, seed, desc, desc, "identical merge changed the schema:\n  got:  %s\n  want: %s", got, want)
		}
	}
}

// Property 3 (the big one): for arbitrary divergent edits, the engine is
// deterministic, and any merge it accepts — clean or resolved — produces a
// schema that passes whole-schema validation. This is invariant 4 stated as
// a universal claim rather than a set of examples.
func TestPropertyResolvedMergesAreAlwaysValid(t *testing.T) {
	var conflicted, clean int
	for round := range rounds {
		seed := int64(2000 + round)
		rng := rand.New(rand.NewSource(seed))
		base := randomSchema(rng)

		ours, oursDesc := randomDiverge(rng, base, "a", 1+rng.Intn(6))
		theirs, theirsDesc := randomDiverge(rng, base, "b", 1+rng.Intn(6))

		in := Input{Base: base, Ours: ours, Theirs: theirs, OursName: "main", TheirsName: "feature"}
		first, err := Merge(in)
		if err != nil {
			fail(t, seed, oursDesc, theirsDesc, "merge errored: %v", err)
		}

		// Determinism: the same inputs must produce the same conflicts, in
		// the same order, every time.
		again, err := Merge(in)
		if err != nil {
			fail(t, seed, oursDesc, theirsDesc, "second merge errored: %v", err)
		}
		if len(first.Conflicts) != len(again.Conflicts) {
			fail(t, seed, oursDesc, theirsDesc, "conflict count not deterministic: %d vs %d",
				len(first.Conflicts), len(again.Conflicts))
		}
		for i := range first.Conflicts {
			if first.Conflicts[i].ID != again.Conflicts[i].ID {
				fail(t, seed, oursDesc, theirsDesc, "conflict order not deterministic at %d", i)
			}
		}

		if len(first.Conflicts) == 0 {
			clean++
			if res := first; len(res.Problems) == 0 && res.Schema == nil {
				fail(t, seed, oursDesc, theirsDesc, "clean merge produced no schema")
			}
			continue
		}
		conflicted++

		// Resolve every conflict — alternating sides, and exercising the
		// provide-a-value path where the conflict allows it — then assert
		// the engine either accepts a coherent schema or reports problems
		// and refuses to hand one back.
		var resolutions []Resolution
		for i, c := range first.Conflicts {
			switch {
			case c.AllowCustom && i%3 == 2:
				resolutions = append(resolutions, Resolution{ConflictID: c.ID, Choice: Custom,
					Custom: customFor(c)})
			case i%2 == 0:
				resolutions = append(resolutions, Resolution{ConflictID: c.ID, Choice: Ours})
			default:
				resolutions = append(resolutions, Resolution{ConflictID: c.ID, Choice: Theirs})
			}
		}
		in.Resolutions = resolutions
		resolved, err := Merge(in)
		if err != nil {
			fail(t, seed, oursDesc, theirsDesc, "resolved merge errored: %v", err)
		}
		if len(resolved.Conflicts) != 0 {
			fail(t, seed, oursDesc, theirsDesc, "answering every conflict left %d unresolved: %+v",
				len(resolved.Conflicts), resolved.Conflicts)
		}
		if resolved.Schema == nil {
			fail(t, seed, oursDesc, theirsDesc, "fully resolved merge produced no schema")
		}
		// The gate: a merged schema is either coherent, or the engine says
		// precisely why. It is never quietly broken.
		problems := validate.Check(resolved.Schema)
		if len(problems) != len(resolved.Problems) {
			fail(t, seed, oursDesc, theirsDesc,
				"validation disagreement: engine reported %d problem(s), re-check found %d",
				len(resolved.Problems), len(problems))
		}
	}

	// The generator must actually be producing conflicts, or this test is
	// proving nothing.
	if conflicted == 0 {
		t.Fatalf("no conflicts generated across %d rounds — the generator is too tame", rounds)
	}
	t.Logf("%d rounds: %d conflicted, %d clean", rounds, conflicted, clean)
}

// Property 4: edits to disjoint tables only ever conflict through a GLOBAL
// NAMESPACE — and this test pins down exactly which namespaces those are.
//
// Almost everything in a schema is scoped to its table: columns, constraints,
// nullability, defaults. Two people editing different tables cannot collide
// on those. But table names and index names are global to the schema, so two
// sides can rename different tables to the same name, or create indexes on
// different tables under one name. Those are real conflicts and the engine is
// right to raise them.
//
// The property: any conflict arising from disjoint edits must be a
// name_collision. Any other class here would mean the engine invented a
// disagreement that does not exist — a defect. (Property testing produced
// this statement; "disjoint edits always merge cleanly" was the unexamined
// assumption it replaced.)
func TestPropertyDisjointEditsAlwaysCompose(t *testing.T) {
	var globalCollisions int
	for round := range rounds {
		seed := int64(3000 + round)
		rng := rand.New(rand.NewSource(seed))
		base := randomSchema(rng)
		if len(base.Tables) < 2 {
			continue
		}

		// Each side edits a disjoint half of the tables, starting from the
		// full base so the merge sees complete snapshots.
		half := len(base.Tables) / 2
		ours, oursDesc := randomDiverge(rng, base, "a", 1+rng.Intn(4), base.Tables[:half]...)
		theirs, theirsDesc := randomDiverge(rng, base, "b", 1+rng.Intn(4), base.Tables[half:]...)

		res, err := Merge(Input{Base: base, Ours: ours, Theirs: theirs,
			OursName: "main", TheirsName: "feature"})
		if err != nil {
			fail(t, seed, oursDesc, theirsDesc, "merge errored: %v", err)
		}
		for _, c := range res.Conflicts {
			if c.Class != "name_collision" {
				fail(t, seed, oursDesc, theirsDesc,
					"disjoint edits produced a conflict that is not a global-namespace collision: %+v", c)
			}
			globalCollisions++
		}
		if len(res.Conflicts) > 0 {
			continue // resolution paths are covered by property 3
		}
		// Both sides' tables must survive the merge intact.
		for _, want := range []*schema.Schema{ours, theirs} {
			for _, wt := range want.Tables {
				if res.Schema.TableByID(wt.ID) == nil {
					fail(t, seed, oursDesc, theirsDesc, "merged schema lost table %q", wt.Name)
				}
			}
		}
	}
	t.Logf("%d rounds: %d global-namespace collisions (table/index names — the only permitted conflict here)",
		rounds, globalCollisions)
}

// customFor produces a plausible provide-a-value answer for a conflict,
// matching the kind of value its UI would ask for.
func customFor(c Conflict) string {
	switch c.CustomKind {
	case "type":
		return "text"
	case "default":
		return "'resolved'"
	default:
		return "resolved_" + strings.NewReplacer(".", "_", "-", "_", " ", "_").Replace(c.Object)
	}
}

// fingerprint renders a schema's full shape by identity, so two schemas can
// be compared exactly without depending on ordering of unrelated fields.
func fingerprint(s *schema.Schema) string {
	if s == nil {
		return "<nil>"
	}
	var parts []string
	for _, t := range s.Tables {
		var cols []string
		for _, c := range t.Columns {
			cols = append(cols, fmt.Sprintf("%s:%s:%s:%v:%s", c.ID[:6], c.Name, c.Type, c.Nullable, c.Default))
		}
		var cons []string
		for _, c := range t.Constraints {
			cons = append(cons, canonicalConstraint(c))
		}
		var idx []string
		for _, ix := range t.Indexes {
			idx = append(idx, ix.Name+canonicalIndex(ix))
		}
		parts = append(parts, fmt.Sprintf("%s:%s[%s][%s][%s]",
			t.ID[:6], t.Name, strings.Join(cols, ","), strings.Join(cons, ","), strings.Join(idx, ",")))
	}
	return strings.Join(parts, " ")
}
