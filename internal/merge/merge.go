// Package merge implements the three-way schema merge. Given the common
// ancestor (base) and the two sides (ours = the target branch, theirs = the
// source branch), every object is matched by stable ID and every property is
// compared by the five-case algebra:
//
//	base A / ours A / theirs A → A       (nobody touched it)
//	base A / ours B / theirs A → B       (only ours changed it)
//	base A / ours A / theirs B → B       (only theirs changed it)
//	base A / ours B / theirs B → B       (both made the same change)
//	base A / ours B / theirs C → CONFLICT (both changed it differently)
//
// The comparison runs per property, not per object — one side renaming a
// column while the other retypes it merges cleanly, because those are
// different properties of the same object.
//
// Conflicts resolve as ours, theirs, or provide-a-value. The third kind is
// first-class because some conflicts have no side to pick: two renames
// colliding into one name need a new name neither side has.
//
// A merged schema with no unresolved conflicts still passes through
// validate.Check — each side can be valid alone while the combination is
// broken (an FK at a table the other side dropped). Those come back as
// Problems, and the caller must not commit while any exist.
package merge

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"mergebase/internal/diff"
	"mergebase/internal/schema"
	"mergebase/internal/validate"
)

type Choice string

const (
	Ours   Choice = "ours"
	Theirs Choice = "theirs"
	Custom Choice = "custom"
)

// Resolution answers one conflict.
type Resolution struct {
	ConflictID string `json:"conflict_id"`
	Choice     Choice `json:"choice"`
	Custom     string `json:"custom,omitempty"` // required when Choice == Custom
}

// Conflict is one place both sides changed the same thing differently.
type Conflict struct {
	ID          string `json:"id"`    // stable — resolutions key on it
	Class       string `json:"class"` // e.g. rename_rename, retype_retype, delete_modify, name_collision
	Table       string `json:"table"`
	Object      string `json:"object,omitempty"`
	Property    string `json:"property"`
	Base        string `json:"base"`
	OursValue   string `json:"ours"`
	TheirsValue string `json:"theirs"`
	Description string `json:"description"`
	AllowCustom bool   `json:"allow_custom"`
	CustomKind  string `json:"custom_kind,omitempty"` // name | type | default — hints the UI input
}

// Input carries the three snapshots and any resolutions.
type Input struct {
	Base, Ours, Theirs   *schema.Schema
	OursName, TheirsName string // display names, e.g. "main", "feature/billing"
	Resolutions          []Resolution
}

// Result of a merge attempt.
type Result struct {
	// Schema is the merged snapshot; set only when no conflicts remain.
	Schema *schema.Schema `json:"schema,omitempty"`
	// Conflicts still needing an answer. Deterministically ordered.
	Conflicts []Conflict `json:"conflicts"`
	// Problems from whole-schema validation of the merged result (only
	// checked once conflicts are resolved). Non-empty blocks the commit.
	Problems []validate.Problem `json:"problems"`
	// Changes is what the merge does to ours (the target), for display.
	Changes []diff.Change `json:"changes"`
}

// Merge performs the three-way merge.
func Merge(in Input) (*Result, error) {
	m := &merger{
		in:          in,
		resolutions: map[string]Resolution{},
	}
	for _, r := range in.Resolutions {
		if r.Choice != Ours && r.Choice != Theirs && r.Choice != Custom {
			return nil, fmt.Errorf("resolution for %q has unknown choice %q", r.ConflictID, r.Choice)
		}
		if r.Choice == Custom && strings.TrimSpace(r.Custom) == "" {
			return nil, fmt.Errorf("resolution for %q chose custom but provided no value", r.ConflictID)
		}
		m.resolutions[r.ConflictID] = r
	}

	merged := m.mergeSchema()

	sort.SliceStable(m.conflicts, func(i, j int) bool {
		a, b := m.conflicts[i], m.conflicts[j]
		if a.Table != b.Table {
			return a.Table < b.Table
		}
		if a.Object != b.Object {
			return a.Object < b.Object
		}
		return a.Property < b.Property
	})

	if m.conflicts == nil {
		m.conflicts = []Conflict{} // JSON contract: always a list, never null
	}
	res := &Result{Conflicts: m.conflicts, Problems: []validate.Problem{}, Changes: []diff.Change{}}
	if m.err != nil {
		return nil, m.err
	}
	if len(m.conflicts) == 0 {
		res.Schema = merged
		res.Problems = validate.Check(merged)
		res.Changes = diff.Compute(in.Ours, merged).Changes
	}
	return res, nil
}

type merger struct {
	in          Input
	resolutions map[string]Resolution
	conflicts   []Conflict
	err         error
}

func (m *merger) conflict(c Conflict) (Resolution, bool) {
	if r, ok := m.resolutions[c.ID]; ok {
		if r.Choice == Custom && !c.AllowCustom {
			m.fail("conflict %q does not accept a custom value", c.ID)
			return Resolution{}, false
		}
		return r, true
	}
	m.conflicts = append(m.conflicts, c)
	return Resolution{}, false
}

func (m *merger) fail(format string, args ...any) {
	if m.err == nil {
		m.err = fmt.Errorf(format, args...)
	}
}

// threeWay applies the five-case algebra to one string property. It returns
// the merged value; when both sides diverge it raises the given conflict and
// (if unresolved) falls back to ours so merging can continue and collect
// every conflict in one pass.
func (m *merger) threeWay(base, ours, theirs string, c Conflict) string {
	switch {
	case ours == theirs:
		return ours
	case base == ours: // only theirs changed
		return theirs
	case base == theirs: // only ours changed
		return ours
	}
	c.Base, c.OursValue, c.TheirsValue = base, ours, theirs
	if r, ok := m.conflict(c); ok {
		switch r.Choice {
		case Ours:
			return ours
		case Theirs:
			return theirs
		case Custom:
			return r.Custom
		}
	}
	return ours
}

// ---- schema level ----

func (m *merger) mergeSchema() *schema.Schema {
	base, ours, theirs := m.in.Base, m.in.Ours, m.in.Theirs
	merged := &schema.Schema{}

	for _, id := range unionTableIDs(base, ours, theirs) {
		bt, ot, tt := base.TableByID(id), ours.TableByID(id), theirs.TableByID(id)
		switch {
		case bt == nil && ot != nil && tt == nil:
			merged.Tables = append(merged.Tables, *ot.Clone())
		case bt == nil && ot == nil && tt != nil:
			merged.Tables = append(merged.Tables, *tt.Clone())
		case bt == nil && ot != nil && tt != nil:
			// Same ID added on both sides is impossible (IDs are random);
			// reaching here means corrupted input.
			m.fail("table %q exists on both sides without a base — identity corruption", ot.Name)
		case bt != nil && ot == nil && tt == nil:
			// dropped on both sides — gone.
		case bt != nil && ot == nil:
			m.dropVsModify(merged, bt, tt, true)
		case bt != nil && tt == nil:
			m.dropVsModify(merged, bt, ot, false)
		default:
			merged.Tables = append(merged.Tables, m.mergeTable(bt, ot, tt))
		}
	}

	m.nameCollisions(merged)
	return merged
}

// dropVsModify handles a table dropped on one side. If the surviving side
// left it untouched, the drop wins silently; if it modified the table, that
// is a delete-vs-modify conflict (C3).
func (m *merger) dropVsModify(merged *schema.Schema, base, survivor *schema.Table, droppedByOurs bool) {
	if tablesEqual(base, survivor) {
		return // drop wins
	}
	modifiedSide := m.in.TheirsName
	oursVal, theirsVal := "dropped", "modified ("+survivor.Name+")"
	if !droppedByOurs {
		modifiedSide = m.in.OursName
		oursVal, theirsVal = "modified ("+survivor.Name+")", "dropped"
	}
	c := Conflict{
		ID: "table_existence:" + string(base.ID), Class: "delete_modify",
		Table: survivor.Name, Property: "existence",
		Base: "exists", OursValue: oursVal, TheirsValue: theirsVal,
		Description: fmt.Sprintf("table %q was dropped on %s but modified on %s — keeping the change means un-dropping the table",
			base.Name, sideName(droppedByOurs, m.in), modifiedSide),
		AllowCustom: false,
	}
	if r, ok := m.conflict(c); ok {
		keepDrop := (r.Choice == Ours) == droppedByOurs
		if !keepDrop {
			merged.Tables = append(merged.Tables, *survivor.Clone())
		}
	}
	// Unresolved: table stays out (ours-fallback when ours dropped it;
	// the conflict is recorded either way, so the fallback never commits).
	if _, ok := m.resolutions[c.ID]; !ok && !droppedByOurs {
		merged.Tables = append(merged.Tables, *survivor.Clone())
	}
}

func sideName(droppedByOurs bool, in Input) string {
	if droppedByOurs {
		return in.OursName
	}
	return in.TheirsName
}

// ---- table level ----

func (m *merger) mergeTable(base, ours, theirs *schema.Table) schema.Table {
	out := schema.Table{ID: base.ID}
	out.Name = m.threeWay(base.Name, ours.Name, theirs.Name, Conflict{
		ID: "table_name:" + string(base.ID), Class: "rename_rename",
		Table: ours.Name, Property: "name",
		Description: fmt.Sprintf("both sides renamed table %q to different names", base.Name),
		AllowCustom: true, CustomKind: "name",
	})

	m.mergeColumns(&out, base, ours, theirs)
	m.mergeConstraints(&out, base, ours, theirs)
	m.mergeIndexes(&out, base, ours, theirs)
	return out
}

func (m *merger) mergeColumns(out *schema.Table, base, ours, theirs *schema.Table) {
	for _, id := range unionColumnIDs(base, ours, theirs) {
		bc, oc, tc := base.ColumnByID(id), ours.ColumnByID(id), theirs.ColumnByID(id)
		switch {
		case bc == nil && oc != nil:
			out.Columns = append(out.Columns, *oc)
		case bc == nil && tc != nil:
			out.Columns = append(out.Columns, *tc)
		case bc != nil && oc == nil && tc == nil:
			// dropped on both sides
		case bc != nil && oc == nil:
			m.columnDropVsModify(out, base, bc, tc, true)
		case bc != nil && tc == nil:
			m.columnDropVsModify(out, base, bc, oc, false)
		default:
			out.Columns = append(out.Columns, m.mergeColumn(base.Name, bc, oc, tc))
		}
	}
	for i := range out.Columns {
		out.Columns[i].Position = i + 1
	}
}

func (m *merger) columnDropVsModify(out *schema.Table, baseTable *schema.Table, base, survivor *schema.Column, droppedByOurs bool) {
	if columnsEqual(base, survivor) {
		return // drop wins
	}
	oursVal, theirsVal := "dropped", describeCol(*survivor)
	if !droppedByOurs {
		oursVal, theirsVal = describeCol(*survivor), "dropped"
	}
	c := Conflict{
		ID: "column_existence:" + string(base.ID), Class: "delete_modify",
		Table: baseTable.Name, Object: base.Name, Property: "existence",
		Base: describeCol(*base), OursValue: oursVal, TheirsValue: theirsVal,
		Description: fmt.Sprintf("column %q was dropped on one side but modified on the other", base.Name),
	}
	if r, ok := m.conflict(c); ok {
		keepDrop := (r.Choice == Ours) == droppedByOurs
		if !keepDrop {
			out.Columns = append(out.Columns, *survivor)
		}
	} else if !droppedByOurs {
		out.Columns = append(out.Columns, *survivor) // ours-fallback, never committed
	}
}

func (m *merger) mergeColumn(table string, base, ours, theirs *schema.Column) schema.Column {
	out := schema.Column{ID: base.ID}

	out.Name = m.threeWay(base.Name, ours.Name, theirs.Name, Conflict{
		ID: "column_name:" + string(base.ID), Class: "rename_rename",
		Table: table, Object: base.Name, Property: "name",
		Description: fmt.Sprintf("both sides renamed column %q to different names", base.Name),
		AllowCustom: true, CustomKind: "name",
	})

	typeStr := m.threeWay(base.Type.String(), ours.Type.String(), theirs.Type.String(), Conflict{
		ID: "column_type:" + string(base.ID), Class: "retype_retype",
		Table: table, Object: out.Name, Property: "type",
		Description: fmt.Sprintf("both sides changed the type of %q differently", base.Name),
		AllowCustom: true, CustomKind: "type",
	})
	dt, err := ParseType(typeStr)
	if err != nil {
		m.fail("invalid type %q for column %q: %v", typeStr, out.Name, err)
		dt = ours.Type
	}
	out.Type = dt

	nullable := m.threeWay(boolStr(base.Nullable), boolStr(ours.Nullable), boolStr(theirs.Nullable), Conflict{
		ID: "column_nullable:" + string(base.ID), Class: "null_null",
		Table: table, Object: out.Name, Property: "nullable",
		Description: fmt.Sprintf("both sides changed the nullability of %q differently", base.Name),
	})
	out.Nullable = nullable == "true"

	out.Default = m.threeWay(base.Default, ours.Default, theirs.Default, Conflict{
		ID: "column_default:" + string(base.ID), Class: "default_default",
		Table: table, Object: out.Name, Property: "default",
		Description: fmt.Sprintf("both sides changed the default of %q differently", base.Name),
		AllowCustom: true, CustomKind: "default",
	})
	return out
}

// ---- constraints ----

func (m *merger) mergeConstraints(out *schema.Table, base, ours, theirs *schema.Table) {
	seenCanonical := map[string]bool{}
	appendIfNew := func(c schema.Constraint) {
		can := canonicalConstraint(c)
		if seenCanonical[can] {
			return // both sides added the structurally identical constraint
		}
		seenCanonical[can] = true
		out.Constraints = append(out.Constraints, c)
	}

	for _, id := range unionConstraintIDs(base, ours, theirs) {
		bc, oc, tc := base.ConstraintByID(id), ours.ConstraintByID(id), theirs.ConstraintByID(id)
		switch {
		case bc == nil && oc != nil:
			appendIfNew(*oc)
		case bc == nil && tc != nil:
			appendIfNew(*tc)
		case bc != nil && oc == nil && tc == nil:
		case bc != nil && oc == nil:
			m.constraintDropVsModify(out, base, bc, tc, true, appendIfNew)
		case bc != nil && tc == nil:
			m.constraintDropVsModify(out, base, bc, oc, false, appendIfNew)
		default:
			ocan, tcan := canonicalConstraint(*oc), canonicalConstraint(*tc)
			bcan := canonicalConstraint(*bc)
			switch {
			case ocan == tcan:
				appendIfNew(*oc)
			case bcan == ocan:
				appendIfNew(*tc)
			case bcan == tcan:
				appendIfNew(*oc)
			default:
				c := Conflict{
					ID: "constraint:" + string(bc.ID), Class: "constraint_constraint",
					Table: out.Name, Object: diff.DescribeConstraint(m.in.Base, base, *bc), Property: "definition",
					Base:      diff.DescribeConstraint(m.in.Base, base, *bc),
					OursValue: diff.DescribeConstraint(m.in.Ours, ours, *oc), TheirsValue: diff.DescribeConstraint(m.in.Theirs, theirs, *tc),
					Description: "both sides changed the same constraint differently",
				}
				if r, ok := m.conflict(c); ok {
					if r.Choice == Theirs {
						appendIfNew(*tc)
					} else {
						appendIfNew(*oc)
					}
				} else {
					appendIfNew(*oc)
				}
			}
		}
	}

	// Two primary keys from different sides (base had none, both added one)
	// is a conflict, not a validation footnote — the user must pick.
	m.resolvePKPair(out, base)
}

func (m *merger) constraintDropVsModify(out *schema.Table, baseTable *schema.Table, base, survivor *schema.Constraint, droppedByOurs bool, appendIfNew func(schema.Constraint)) {
	if canonicalConstraint(*base) == canonicalConstraint(*survivor) {
		return // drop wins over untouched
	}
	def := diff.DescribeConstraint(m.in.Base, baseTable, *base)
	oursVal, theirsVal := "dropped", "modified"
	if !droppedByOurs {
		oursVal, theirsVal = "modified", "dropped"
	}
	c := Conflict{
		ID: "constraint_existence:" + string(base.ID), Class: "delete_modify",
		Table: out.Name, Object: def, Property: "existence",
		Base: def, OursValue: oursVal, TheirsValue: theirsVal,
		Description: "a constraint was dropped on one side but modified on the other",
	}
	if r, ok := m.conflict(c); ok {
		keepDrop := (r.Choice == Ours) == droppedByOurs
		if !keepDrop {
			appendIfNew(*survivor)
		}
	} else if !droppedByOurs {
		appendIfNew(*survivor)
	}
}

func (m *merger) resolvePKPair(out *schema.Table, base *schema.Table) {
	var pks []int
	for i, c := range out.Constraints {
		if c.Kind == schema.PrimaryKey {
			pks = append(pks, i)
		}
	}
	if len(pks) < 2 || base.PrimaryKey() != nil {
		return
	}
	first, second := out.Constraints[pks[0]], out.Constraints[pks[1]]
	c := Conflict{
		ID: "pk:" + string(out.ID), Class: "pk_pk",
		Table: out.Name, Property: "primary_key",
		Base:      "none",
		OursValue: describeConstraintOn(out, first), TheirsValue: describeConstraintOn(out, second),
		Description: fmt.Sprintf("both sides added a primary key to %q — a table has exactly one", out.Name),
	}
	drop := pks[1] // default keep ours' (first added)
	if r, ok := m.conflict(c); ok && r.Choice == Theirs {
		drop = pks[0]
	}
	out.Constraints = append(out.Constraints[:drop], out.Constraints[drop+1:]...)
}

// ---- indexes ----

func (m *merger) mergeIndexes(out *schema.Table, base, ours, theirs *schema.Table) {
	for _, id := range unionIndexIDs(base, ours, theirs) {
		bx, ox, tx := base.IndexByID(id), ours.IndexByID(id), theirs.IndexByID(id)
		switch {
		case bx == nil && ox != nil:
			out.Indexes = append(out.Indexes, *ox)
		case bx == nil && tx != nil:
			out.Indexes = append(out.Indexes, *tx)
		case bx != nil && ox == nil && tx == nil:
		case bx != nil && ox == nil:
			m.indexDropVsModify(out, bx, tx, true)
		case bx != nil && tx == nil:
			m.indexDropVsModify(out, bx, ox, false)
		default:
			ocan, tcan := canonicalIndex(*ox), canonicalIndex(*tx)
			bcan := canonicalIndex(*bx)
			switch {
			case ocan == tcan:
				out.Indexes = append(out.Indexes, *ox)
			case bcan == ocan:
				out.Indexes = append(out.Indexes, *tx)
			case bcan == tcan:
				out.Indexes = append(out.Indexes, *ox)
			default:
				c := Conflict{
					ID: "index:" + string(bx.ID), Class: "index_index",
					Table: out.Name, Object: bx.Name, Property: "definition",
					Base:      diff.DescribeIndex(base, *bx),
					OursValue: diff.DescribeIndex(ours, *ox), TheirsValue: diff.DescribeIndex(theirs, *tx),
					Description: fmt.Sprintf("both sides changed index %q differently", bx.Name),
				}
				if r, ok := m.conflict(c); ok && r.Choice == Theirs {
					out.Indexes = append(out.Indexes, *tx)
				} else {
					out.Indexes = append(out.Indexes, *ox)
				}
			}
		}
	}
}

func (m *merger) indexDropVsModify(out *schema.Table, base, survivor *schema.Index, droppedByOurs bool) {
	if canonicalIndex(*base) == canonicalIndex(*survivor) && base.Name == survivor.Name {
		return
	}
	oursVal, theirsVal := "dropped", "modified"
	if !droppedByOurs {
		oursVal, theirsVal = "modified", "dropped"
	}
	c := Conflict{
		ID: "index_existence:" + string(base.ID), Class: "delete_modify",
		Table: out.Name, Object: base.Name, Property: "existence",
		Base: base.Name, OursValue: oursVal, TheirsValue: theirsVal,
		Description: fmt.Sprintf("index %q was dropped on one side but modified on the other", base.Name),
	}
	if r, ok := m.conflict(c); ok {
		keepDrop := (r.Choice == Ours) == droppedByOurs
		if !keepDrop {
			out.Indexes = append(out.Indexes, *survivor)
		}
	} else if !droppedByOurs {
		out.Indexes = append(out.Indexes, *survivor)
	}
}

// ---- name collisions across objects (C4 and the rename-collision case) ----
//
// Two different objects claiming one name — add/add, or two renames landing
// on the same name — cannot be answered by ours/theirs on a property: the
// user keeps one, or renames one. Detected on the merged result so every
// path that can create a collision is covered by one mechanism.

func (m *merger) nameCollisions(merged *schema.Schema) {
	// Tables.
	byName := map[string]*schema.Table{}
	keepTables := merged.Tables[:0]
	for i := range merged.Tables {
		t := &merged.Tables[i]
		first, dup := byName[t.Name]
		if !dup {
			byName[t.Name] = t
			keepTables = append(keepTables, *t)
			continue
		}
		c := Conflict{
			ID: "name_collision:" + string(first.ID) + ":" + string(t.ID), Class: "name_collision",
			Table: t.Name, Property: "name",
			Base:        "no table named " + t.Name,
			OursValue:   fmt.Sprintf("%s (%d columns)", first.Name, len(first.Columns)),
			TheirsValue: fmt.Sprintf("%s (%d columns)", t.Name, len(t.Columns)),
			Description: fmt.Sprintf("two different tables ended up named %q — keep one, or give the second a new name", t.Name),
			AllowCustom: true, CustomKind: "name",
		}
		if r, ok := m.conflict(c); ok {
			switch r.Choice {
			case Ours: // keep first, drop second
			case Theirs: // drop first, keep second
				keepTables[len(keepTables)-1] = *t
				byName[t.Name] = &keepTables[len(keepTables)-1]
			case Custom:
				t2 := *t
				t2.Name = r.Custom
				keepTables = append(keepTables, t2)
			}
		} else {
			keepTables = append(keepTables, *t) // stays for later validation; conflict recorded
		}
	}
	merged.Tables = keepTables

	// Columns within each table.
	for ti := range merged.Tables {
		t := &merged.Tables[ti]
		seen := map[string]*schema.Column{}
		keep := t.Columns[:0]
		for i := range t.Columns {
			col := &t.Columns[i]
			first, dup := seen[col.Name]
			if !dup {
				seen[col.Name] = col
				keep = append(keep, *col)
				continue
			}
			c := Conflict{
				ID: "name_collision:" + string(first.ID) + ":" + string(col.ID), Class: "name_collision",
				Table: t.Name, Object: col.Name, Property: "name",
				Base:      "no column named " + col.Name,
				OursValue: describeCol(*first), TheirsValue: describeCol(*col),
				Description: fmt.Sprintf("two different columns on %q ended up named %q — keep one, or give the second a new name", t.Name, col.Name),
				AllowCustom: true, CustomKind: "name",
			}
			if r, ok := m.conflict(c); ok {
				switch r.Choice {
				case Ours:
				case Theirs:
					keep[len(keep)-1] = *col
				case Custom:
					c2 := *col
					c2.Name = r.Custom
					keep = append(keep, c2)
				}
			} else {
				keep = append(keep, *col)
			}
		}
		t.Columns = keep
		for i := range t.Columns {
			t.Columns[i].Position = i + 1
		}
	}

	// Index names are global; validation reports leftovers, but two fresh
	// indexes claiming one name get a proper conflict here.
	type ixRef struct {
		table *schema.Table
		idx   int
	}
	ixByName := map[string]ixRef{}
	for ti := range merged.Tables {
		t := &merged.Tables[ti]
		for i := 0; i < len(t.Indexes); i++ {
			ix := t.Indexes[i]
			first, dup := ixByName[ix.Name]
			if !dup {
				ixByName[ix.Name] = ixRef{t, i}
				continue
			}
			c := Conflict{
				ID: "name_collision:" + string(first.table.Indexes[first.idx].ID) + ":" + string(ix.ID), Class: "name_collision",
				Table: t.Name, Object: ix.Name, Property: "name",
				Base:      "no index named " + ix.Name,
				OursValue: diff.DescribeIndex(first.table, first.table.Indexes[first.idx]), TheirsValue: diff.DescribeIndex(t, ix),
				Description: fmt.Sprintf("two different indexes ended up named %q — keep one, or give the second a new name", ix.Name),
				AllowCustom: true, CustomKind: "name",
			}
			if r, ok := m.conflict(c); ok {
				switch r.Choice {
				case Ours:
					t.Indexes = append(t.Indexes[:i], t.Indexes[i+1:]...)
					i--
				case Theirs:
					first.table.Indexes = append(first.table.Indexes[:first.idx], first.table.Indexes[first.idx+1:]...)
					ixByName[ix.Name] = ixRef{t, i}
				case Custom:
					t.Indexes[i].Name = r.Custom
				}
			}
		}
	}
}

// ---- helpers ----

func unionTableIDs(base, ours, theirs *schema.Schema) []schema.ObjectID {
	return unionIDs(
		ids(base.Tables, func(t schema.Table) schema.ObjectID { return t.ID }),
		ids(ours.Tables, func(t schema.Table) schema.ObjectID { return t.ID }),
		ids(theirs.Tables, func(t schema.Table) schema.ObjectID { return t.ID }),
	)
}

func unionColumnIDs(base, ours, theirs *schema.Table) []schema.ObjectID {
	return unionIDs(
		ids(base.Columns, func(c schema.Column) schema.ObjectID { return c.ID }),
		ids(ours.Columns, func(c schema.Column) schema.ObjectID { return c.ID }),
		ids(theirs.Columns, func(c schema.Column) schema.ObjectID { return c.ID }),
	)
}

func unionConstraintIDs(base, ours, theirs *schema.Table) []schema.ObjectID {
	return unionIDs(
		ids(base.Constraints, func(c schema.Constraint) schema.ObjectID { return c.ID }),
		ids(ours.Constraints, func(c schema.Constraint) schema.ObjectID { return c.ID }),
		ids(theirs.Constraints, func(c schema.Constraint) schema.ObjectID { return c.ID }),
	)
}

func unionIndexIDs(base, ours, theirs *schema.Table) []schema.ObjectID {
	return unionIDs(
		ids(base.Indexes, func(i schema.Index) schema.ObjectID { return i.ID }),
		ids(ours.Indexes, func(i schema.Index) schema.ObjectID { return i.ID }),
		ids(theirs.Indexes, func(i schema.Index) schema.ObjectID { return i.ID }),
	)
}

func ids[T any](items []T, id func(T) schema.ObjectID) []schema.ObjectID {
	out := make([]schema.ObjectID, 0, len(items))
	for _, it := range items {
		out = append(out, id(it))
	}
	return out
}

// unionIDs preserves first-seen order: base order, then ours additions, then
// theirs additions — deterministic merged layout.
func unionIDs(lists ...[]schema.ObjectID) []schema.ObjectID {
	seen := map[schema.ObjectID]bool{}
	var out []schema.ObjectID
	for _, list := range lists {
		for _, id := range list {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// canonicalConstraint serializes a constraint's meaning using IDs, not
// names, so a rename on either side never reads as a constraint change.
func canonicalConstraint(c schema.Constraint) string {
	parts := []string{string(c.Kind)}
	for _, id := range c.ColumnIDs {
		parts = append(parts, string(id))
	}
	parts = append(parts, "→", string(c.RefTableID))
	for _, id := range c.RefColumnIDs {
		parts = append(parts, string(id))
	}
	parts = append(parts, string(c.OnDelete), string(c.OnUpdate), c.Expr)
	return strings.Join(parts, "|")
}

func canonicalIndex(ix schema.Index) string {
	parts := []string{}
	for _, ic := range ix.Columns {
		parts = append(parts, string(ic.ColumnID)+":"+boolStr(ic.Desc))
	}
	parts = append(parts, boolStr(ix.Unique), ix.Method)
	return strings.Join(parts, "|")
}

func tablesEqual(a, b *schema.Table) bool {
	if a.Name != b.Name || len(a.Columns) != len(b.Columns) ||
		len(a.Constraints) != len(b.Constraints) || len(a.Indexes) != len(b.Indexes) {
		return false
	}
	for i := range a.Columns {
		if !columnsEqual(&a.Columns[i], &b.Columns[i]) {
			return false
		}
	}
	for i := range a.Constraints {
		if canonicalConstraint(a.Constraints[i]) != canonicalConstraint(b.Constraints[i]) {
			return false
		}
	}
	for i := range a.Indexes {
		if a.Indexes[i].Name != b.Indexes[i].Name || canonicalIndex(a.Indexes[i]) != canonicalIndex(b.Indexes[i]) {
			return false
		}
	}
	return true
}

func columnsEqual(a, b *schema.Column) bool {
	return a.ID == b.ID && a.Name == b.Name && a.Type.Equal(b.Type) &&
		a.Nullable == b.Nullable && a.Default == b.Default
}

// describeConstraintOn renders a constraint against a single table's own
// columns (enough for same-table constraints like primary keys).
func describeConstraintOn(t *schema.Table, c schema.Constraint) string {
	names := make([]string, 0, len(c.ColumnIDs))
	for _, id := range c.ColumnIDs {
		if col := t.ColumnByID(id); col != nil {
			names = append(names, col.Name)
		} else {
			names = append(names, "?")
		}
	}
	return strings.ToUpper(strings.ReplaceAll(string(c.Kind), "_", " ")) + " (" + strings.Join(names, ", ") + ")"
}

func describeCol(c schema.Column) string {
	out := c.Name + " " + c.Type.String()
	if !c.Nullable {
		out += " NOT NULL"
	}
	if c.Default != "" {
		out += " DEFAULT " + c.Default
	}
	return out
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

var typeRe = regexp.MustCompile(`^\s*([a-zA-Z ]+?)\s*(?:\(\s*(\d+)\s*(?:,\s*(\d+)\s*)?\))?\s*$`)

// ParseType parses a rendered type like "varchar(255)" or "numeric(10,2)" —
// the inverse of DataType.String, used for custom type resolutions.
func ParseType(s string) (schema.DataType, error) {
	match := typeRe.FindStringSubmatch(s)
	if match == nil || strings.TrimSpace(match[1]) == "" {
		return schema.DataType{}, fmt.Errorf("cannot parse type %q — expected e.g. text, varchar(255), numeric(10,2)", s)
	}
	dt := schema.DataType{Base: strings.ToLower(strings.TrimSpace(match[1]))}
	for _, p := range []string{match[2], match[3]} {
		if p != "" {
			n, err := strconv.Atoi(p)
			if err != nil {
				return schema.DataType{}, fmt.Errorf("bad type parameter %q", p)
			}
			dt.Params = append(dt.Params, n)
		}
	}
	return dt, nil
}
