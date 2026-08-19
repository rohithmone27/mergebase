// Package match aligns a freshly parsed DDL import with the branch's current
// head snapshot, transplanting stable IDs so identity survives re-import.
//
// This is the half of the identity story that operation capture cannot
// cover: pasted DDL carries no IDs. Two rules govern it, both load-bearing:
//
//   - The matcher always resolves against the branch's OWN HEAD, never from
//     scratch (invariant 7). If a re-import minted fresh IDs for existing
//     objects, that branch would silently drift from its siblings and every
//     future merge would collapse into add/add noise.
//   - The system never silently invents identity. Same name ⇒ same object is
//     safe automation; anything less than an exact name match is only ever
//     PROPOSED — a candidate rename with a confidence score — and the user
//     confirms rename vs drop-and-add. A wrong silent guess is a destructive
//     interpretation (a rename preserves data; a drop+add destroys it).
package match

import (
	"sort"

	"mergebase/internal/schema"
)

// Proposal is one candidate rename the user must confirm or reject.
type Proposal struct {
	Kind       string          `json:"kind"` // "table" | "column"
	Table      string          `json:"table,omitempty"`
	OldID      schema.ObjectID `json:"old_id"`
	OldName    string          `json:"old_name"`
	NewID      schema.ObjectID `json:"new_id"`
	NewName    string          `json:"new_name"`
	Confidence float64         `json:"confidence"` // 0..1; UI preselects "rename" when high
	Detail     string          `json:"detail"`
}

// Decision is the user's answer to one proposal. It keys on the head-side
// OldID: fresh import IDs change on every re-parse, so they cannot carry
// decisions across rounds — the head ID is the stable handle. The matcher
// re-derives the same best candidate deterministically and applies the
// answer to it.
type Decision struct {
	OldID  schema.ObjectID `json:"old_id"`
	Rename bool            `json:"rename"` // false = keep as drop + add
}

// Outcome of matching an import against the head snapshot.
type Outcome struct {
	// Schema is the imported snapshot with IDs transplanted for every exact
	// match and every confirmed rename.
	Schema *schema.Schema
	// Proposals still needing a decision. Empty means the import is ready
	// to commit.
	Proposals []Proposal
}

// Rematch aligns imported (fresh IDs from the parser) with head. Decisions
// answer previously returned proposals; pass none on the first call.
func Rematch(head, imported *schema.Schema, decisions []Decision) *Outcome {
	out := imported.Clone()
	remap := map[schema.ObjectID]schema.ObjectID{} // fresh imported ID → head ID
	accepted := map[schema.ObjectID]bool{}         // head oldID: rename confirmed
	rejected := map[schema.ObjectID]bool{}         // head oldID: rename declined
	for _, d := range decisions {
		if d.Rename {
			accepted[d.OldID] = true
		} else {
			rejected[d.OldID] = true
		}
	}

	o := &Outcome{Schema: out, Proposals: []Proposal{}}

	// --- tables: exact names auto-match; the rest become proposals ---
	matchedHead := map[schema.ObjectID]*schema.Table{} // headID → imported table
	var unmatchedHead []*schema.Table
	var unmatchedNew []*schema.Table
	for i := range head.Tables {
		ht := &head.Tables[i]
		if nt := out.TableByName(ht.Name); nt != nil {
			remap[nt.ID] = ht.ID
			nt.ID = ht.ID
			matchedHead[ht.ID] = nt
		} else {
			unmatchedHead = append(unmatchedHead, ht)
		}
	}
	for i := range out.Tables {
		nt := &out.Tables[i]
		if _, taken := matchedHead[nt.ID]; !taken {
			if head.TableByID(nt.ID) == nil { // still carries a fresh ID
				unmatchedNew = append(unmatchedNew, nt)
			}
		}
	}

	claimed := map[schema.ObjectID]bool{} // fresh IDs already claimed by a match
	for _, ht := range unmatchedHead {
		best, bestScore := (*schema.Table)(nil), 0.0
		for _, nt := range unmatchedNew {
			if claimed[nt.ID] {
				continue
			}
			score := tableSimilarity(ht, nt)
			if score > bestScore {
				best, bestScore = nt, score
			}
		}
		if best == nil || bestScore < 0.4 {
			continue // genuinely dropped (or brand new on the other side)
		}
		switch {
		case accepted[ht.ID]:
			claimed[best.ID] = true
			transplantTable(ht, best, remap)
		case rejected[ht.ID]:
			// keep as drop + add
		default:
			o.Proposals = append(o.Proposals, Proposal{
				Kind: "table", OldID: ht.ID, OldName: ht.Name,
				NewID: best.ID, NewName: best.Name, Confidence: round2(bestScore),
				Detail: "columns overlap suggests this table was renamed rather than dropped and re-created",
			})
		}
	}

	// --- columns within name/decision-matched tables ---
	for i := range head.Tables {
		ht := &head.Tables[i]
		nt := out.TableByID(ht.ID)
		if nt == nil {
			continue
		}
		o.matchColumns(ht, nt, accepted, rejected, remap)
	}

	// Constraints and indexes in the import still reference the fresh IDs;
	// rewrite every reference into head-ID space, then transplant member
	// identities for structurally identical constraints and indexes.
	rewriteRefs(out, remap)
	for i := range head.Tables {
		ht := &head.Tables[i]
		if nt := out.TableByID(ht.ID); nt != nil {
			transplantMembers(ht, nt)
		}
	}

	sort.SliceStable(o.Proposals, func(i, j int) bool {
		a, b := o.Proposals[i], o.Proposals[j]
		if a.Table != b.Table {
			return a.Table < b.Table
		}
		return a.OldName < b.OldName
	})
	return o
}

func (o *Outcome) matchColumns(ht, nt *schema.Table, accepted, rejected map[schema.ObjectID]bool, remap map[schema.ObjectID]schema.ObjectID) {
	// Exact names first.
	var unmatchedOld []*schema.Column
	usedNew := map[schema.ObjectID]bool{}
	for i := range ht.Columns {
		hc := &ht.Columns[i]
		if nc := nt.ColumnByName(hc.Name); nc != nil {
			remap[nc.ID] = hc.ID
			nc.ID = hc.ID
			usedNew[nc.ID] = true
		} else {
			unmatchedOld = append(unmatchedOld, hc)
		}
	}
	// Score the leftovers; decisions key on the head-side ID.
	for _, hc := range unmatchedOld {
		var best *schema.Column
		bestScore := 0.0
		for i := range nt.Columns {
			nc := &nt.Columns[i]
			if usedNew[nc.ID] || ht.ColumnByID(nc.ID) != nil {
				continue
			}
			score := columnSimilarity(hc, nc, len(ht.Columns))
			if score > bestScore {
				best, bestScore = nc, score
			}
		}
		if best == nil || bestScore < 0.5 {
			continue
		}
		switch {
		case accepted[hc.ID]:
			remap[best.ID] = hc.ID
			best.ID = hc.ID
			usedNew[hc.ID] = true
		case rejected[hc.ID]:
			// keep as drop + add
		default:
			o.Proposals = append(o.Proposals, Proposal{
				Kind: "column", Table: nt.Name,
				OldID: hc.ID, OldName: hc.Name, NewID: best.ID, NewName: best.Name,
				Confidence: round2(bestScore),
				Detail:     "type and position suggest a rename — confirming preserves the column's history and data",
			})
		}
	}
}

// transplantTable moves the head table's identity onto the imported one.
func transplantTable(ht, nt *schema.Table, remap map[schema.ObjectID]schema.ObjectID) {
	remap[nt.ID] = ht.ID
	nt.ID = ht.ID
	for i := range nt.Columns {
		if hc := ht.ColumnByName(nt.Columns[i].Name); hc != nil {
			remap[nt.Columns[i].ID] = hc.ID
			nt.Columns[i].ID = hc.ID
		}
	}
}

// rewriteRefs maps every constraint and index reference through remap, so
// references follow the transplanted identities.
func rewriteRefs(s *schema.Schema, remap map[schema.ObjectID]schema.ObjectID) {
	mapID := func(id schema.ObjectID) schema.ObjectID {
		if to, ok := remap[id]; ok {
			return to
		}
		return id
	}
	for ti := range s.Tables {
		t := &s.Tables[ti]
		for ci := range t.Constraints {
			c := &t.Constraints[ci]
			for i := range c.ColumnIDs {
				c.ColumnIDs[i] = mapID(c.ColumnIDs[i])
			}
			c.RefTableID = mapID(c.RefTableID)
			for i := range c.RefColumnIDs {
				c.RefColumnIDs[i] = mapID(c.RefColumnIDs[i])
			}
		}
		for ii := range t.Indexes {
			ix := &t.Indexes[ii]
			for i := range ix.Columns {
				ix.Columns[i].ColumnID = mapID(ix.Columns[i].ColumnID)
			}
		}
	}
}

// transplantMembers reuses head IDs for structurally identical constraints
// and same-name indexes, so unchanged members do not read as drop+add.
func transplantMembers(ht, nt *schema.Table) {
	for i := range nt.Constraints {
		nc := &nt.Constraints[i]
		for j := range ht.Constraints {
			hc := &ht.Constraints[j]
			if hc.Kind == nc.Kind && sameIDs(hc.ColumnIDs, nc.ColumnIDs) &&
				hc.RefTableID == nc.RefTableID && sameIDs(hc.RefColumnIDs, nc.RefColumnIDs) && hc.Expr == nc.Expr {
				nc.ID = hc.ID
				break
			}
		}
	}
	for i := range nt.Indexes {
		ni := &nt.Indexes[i]
		for j := range ht.Indexes {
			if ht.Indexes[j].Name == ni.Name {
				ni.ID = ht.Indexes[j].ID
				break
			}
		}
	}
}

func sameIDs(a, b []schema.ObjectID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// tableSimilarity scores how much of the head table survives in the
// candidate: fraction of columns matching by name and type.
func tableSimilarity(a, b *schema.Table) float64 {
	if len(a.Columns) == 0 || len(b.Columns) == 0 {
		return 0
	}
	matches := 0
	for _, ac := range a.Columns {
		if bc := b.ColumnByName(ac.Name); bc != nil && bc.Type.Equal(ac.Type) {
			matches++
		}
	}
	return float64(2*matches) / float64(len(a.Columns)+len(b.Columns))
}

// columnSimilarity combines type match, position, and name similarity —
// the constraint fingerprint of a plausible rename.
func columnSimilarity(a, b *schema.Column, tableSize int) float64 {
	score := 0.0
	if a.Type.Equal(b.Type) {
		score += 0.5
	} else if a.Type.Base == b.Type.Base {
		score += 0.3
	}
	if a.Position == b.Position {
		score += 0.2
	} else if tableSize > 0 && abs(a.Position-b.Position) == 1 {
		score += 0.1
	}
	score += 0.3 * nameSimilarity(a.Name, b.Name)
	if score > 1 {
		score = 1
	}
	return score
}

// nameSimilarity is 1 - normalized Levenshtein distance.
func nameSimilarity(a, b string) float64 {
	if a == b {
		return 1
	}
	la, lb := len(a), len(b)
	if la == 0 || lb == 0 {
		return 0
	}
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, min(cur[j-1]+1, prev[j-1]+cost))
		}
		prev, cur = cur, prev
	}
	dist := prev[lb]
	longer := max(la, lb)
	return 1 - float64(dist)/float64(longer)
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }
