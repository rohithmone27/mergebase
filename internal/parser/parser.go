// Package parser turns PostgreSQL DDL into a schema model.
//
// It uses the real PostgreSQL grammar (via pg_query_go), then maps the parse
// tree onto the documented subset the model supports. The fidelity policy:
// parse everything, model the subset, and record every construct that was
// recognized but not modeled — import warns, and export can state what it
// does not cover. Nothing is silently dropped.
package parser

import (
	"fmt"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"

	"mergebase/internal/schema"
)

// Result is a parsed schema plus everything the model could not represent.
type Result struct {
	Schema *schema.Schema
	// Unsupported lists recognized-but-not-modeled constructs, in input order.
	Unsupported []Unsupported
}

// Unsupported records one construct the model does not represent.
type Unsupported struct {
	Construct string `json:"construct"` // e.g. "CREATE SEQUENCE", "partial index"
	Detail    string `json:"detail"`    // object name or explanation
}

// Parse converts DDL into a schema model. Statements the model cannot
// represent are recorded in Result.Unsupported rather than dropped. A
// reference to a table or column that does not exist in the DDL is an error.
func Parse(ddl string) (*Result, error) {
	tree, err := pg_query.Parse(ddl)
	if err != nil {
		return nil, fmt.Errorf("invalid SQL: %w", err)
	}

	p := &parseRun{schema: &schema.Schema{}}
	for _, raw := range tree.Stmts {
		p.statement(raw.Stmt)
	}
	if p.err != nil {
		return nil, p.err
	}
	if err := p.resolve(); err != nil {
		return nil, err
	}
	return &Result{Schema: p.schema, Unsupported: p.unsupported}, nil
}

type parseRun struct {
	schema         *schema.Schema
	unsupported    []Unsupported
	pendingFKs     []pendingFK
	pendingIndexes []pendingIndex
	err            error
}

// pendingFK is a foreign key whose target is still known only by name.
// Resolution to IDs happens after all tables have been parsed, so that
// forward references between tables work.
type pendingFK struct {
	tableName  string
	constraint schema.Constraint // Kind/Name/OnDelete/OnUpdate filled in
	columns    []string          // local column names
	refTable   string
	refColumns []string // empty means "the target's primary key"
}

func (p *parseRun) fail(format string, args ...any) {
	if p.err == nil {
		p.err = fmt.Errorf(format, args...)
	}
}

func (p *parseRun) skip(construct, detail string) {
	p.unsupported = append(p.unsupported, Unsupported{Construct: construct, Detail: detail})
}

func (p *parseRun) statement(n *pg_query.Node) {
	switch {
	case n.GetCreateStmt() != nil:
		p.createTable(n.GetCreateStmt())
	case n.GetIndexStmt() != nil:
		p.createIndex(n.GetIndexStmt())
	case n.GetAlterTableStmt() != nil:
		p.alterTable(n.GetAlterTableStmt())
	default:
		p.skip(statementLabel(n), "")
	}
}

// statementLabel names an unsupported statement for the fidelity report.
func statementLabel(n *pg_query.Node) string {
	name := fmt.Sprintf("%T", n.Node)
	name = strings.TrimPrefix(name, "*pg_query.Node_")
	name = strings.TrimSuffix(name, "Stmt")
	// CamelCase → words: "CreateSeq" → "CREATE SEQ".
	var b strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return strings.ToUpper(b.String())
}

func (p *parseRun) tableName(rv *pg_query.RangeVar) string {
	if rv.Schemaname != "" && rv.Schemaname != "public" {
		p.skip("schema-qualified name", rv.Schemaname+"."+rv.Relname+" (imported as "+rv.Relname+")")
	}
	return rv.Relname
}

func (p *parseRun) createTable(st *pg_query.CreateStmt) {
	name := p.tableName(st.Relation)
	if p.schema.TableByName(name) != nil {
		p.fail("duplicate table %q in DDL", name)
		return
	}
	if st.Partspec != nil {
		p.skip("partitioned table", name+" (imported without partitioning)")
	}
	if len(st.InhRelations) > 0 {
		p.fail("table %q uses inheritance/partition-of, which is not supported", name)
		return
	}

	table := schema.Table{ID: schema.NewObjectID(), Name: name}
	for _, elt := range st.TableElts {
		switch {
		case elt.GetColumnDef() != nil:
			p.column(&table, elt.GetColumnDef())
		case elt.GetConstraint() != nil:
			p.constraint(&table, elt.GetConstraint(), nil)
		case elt.GetTableLikeClause() != nil:
			p.skip("LIKE clause", "in table "+name)
		default:
			p.skip("table element", fmt.Sprintf("%T in table %s", elt.Node, name))
		}
	}
	if p.err != nil {
		return
	}
	p.schema.Tables = append(p.schema.Tables, table)
}

func (p *parseRun) column(table *schema.Table, cd *pg_query.ColumnDef) {
	if table.ColumnByName(cd.Colname) != nil {
		p.fail("duplicate column %q in table %q", cd.Colname, table.Name)
		return
	}
	col := schema.Column{
		ID:       schema.NewObjectID(),
		Name:     cd.Colname,
		Nullable: true,
		Position: len(table.Columns) + 1,
	}
	dt, ok := p.dataType(cd.TypeName, table.Name, cd.Colname)
	if !ok {
		return
	}
	col.Type = dt

	if cd.CollClause != nil {
		p.skip("COLLATE clause", table.Name+"."+cd.Colname)
	}
	if cd.Identity != "" {
		p.skip("identity column", table.Name+"."+cd.Colname+" (imported as plain column)")
	}
	if cd.Generated != "" {
		p.skip("generated column", table.Name+"."+cd.Colname+" (generation expression dropped)")
	}
	if cd.RawDefault != nil {
		expr, err := deparseExpr(cd.RawDefault)
		if err != nil {
			p.fail("deparsing default for %s.%s: %v", table.Name, cd.Colname, err)
			return
		}
		col.Default = expr
	}

	table.Columns = append(table.Columns, col)

	for _, cn := range cd.Constraints {
		c := cn.GetConstraint()
		if c == nil {
			continue
		}
		switch c.Contype {
		case pg_query.ConstrType_CONSTR_NOTNULL:
			table.Columns[len(table.Columns)-1].Nullable = false
		case pg_query.ConstrType_CONSTR_NULL:
			table.Columns[len(table.Columns)-1].Nullable = true
		case pg_query.ConstrType_CONSTR_DEFAULT:
			expr, err := deparseExpr(c.RawExpr)
			if err != nil {
				p.fail("deparsing default for %s.%s: %v", table.Name, cd.Colname, err)
				return
			}
			table.Columns[len(table.Columns)-1].Default = expr
		default:
			// Inline PK/UNIQUE/FK/CHECK are the table-level forms scoped to
			// this column.
			p.constraint(table, c, []string{cd.Colname})
		}
	}
}

// constraint maps one constraint. inlineCols is set for constraints written
// inline on a column definition, where the parse tree omits the column list.
func (p *parseRun) constraint(table *schema.Table, c *pg_query.Constraint, inlineCols []string) {
	colNames := inlineCols
	if len(colNames) == 0 {
		colNames = stringList(c.Keys)
	}

	switch c.Contype {
	case pg_query.ConstrType_CONSTR_PRIMARY:
		ids, ok := p.columnIDs(table, colNames)
		if !ok {
			return
		}
		// Primary key members are NOT NULL by PostgreSQL semantics.
		for _, id := range ids {
			table.ColumnByID(id).Nullable = false
		}
		table.Constraints = append(table.Constraints, schema.Constraint{
			ID: schema.NewObjectID(), Name: c.Conname, Kind: schema.PrimaryKey, ColumnIDs: ids,
		})

	case pg_query.ConstrType_CONSTR_UNIQUE:
		if c.NullsNotDistinct {
			p.skip("UNIQUE NULLS NOT DISTINCT", "on "+table.Name+" (imported as plain UNIQUE)")
		}
		ids, ok := p.columnIDs(table, colNames)
		if !ok {
			return
		}
		table.Constraints = append(table.Constraints, schema.Constraint{
			ID: schema.NewObjectID(), Name: c.Conname, Kind: schema.Unique, ColumnIDs: ids,
		})

	case pg_query.ConstrType_CONSTR_CHECK:
		expr, err := deparseExpr(c.RawExpr)
		if err != nil {
			p.fail("deparsing CHECK on %s: %v", table.Name, err)
			return
		}
		var ids []schema.ObjectID
		if len(inlineCols) > 0 {
			var ok bool
			if ids, ok = p.columnIDs(table, inlineCols); !ok {
				return
			}
		}
		table.Constraints = append(table.Constraints, schema.Constraint{
			ID: schema.NewObjectID(), Name: c.Conname, Kind: schema.Check, ColumnIDs: ids, Expr: expr,
		})

	case pg_query.ConstrType_CONSTR_FOREIGN:
		fkCols := inlineCols
		if len(fkCols) == 0 {
			fkCols = stringList(c.FkAttrs)
		}
		p.pendingFKs = append(p.pendingFKs, pendingFK{
			tableName: table.Name,
			constraint: schema.Constraint{
				ID:       schema.NewObjectID(),
				Name:     c.Conname,
				Kind:     schema.ForeignKey,
				OnDelete: refAction(c.FkDelAction),
				OnUpdate: refAction(c.FkUpdAction),
			},
			columns:    fkCols,
			refTable:   p.tableName(c.Pktable),
			refColumns: stringList(c.PkAttrs),
		})

	case pg_query.ConstrType_CONSTR_EXCLUSION:
		p.skip("EXCLUDE constraint", "on "+table.Name)
	default:
		p.skip("constraint", fmt.Sprintf("%s on %s", c.Contype, table.Name))
	}
}

func (p *parseRun) createIndex(st *pg_query.IndexStmt) {
	tableName := p.tableName(st.Relation)
	if st.WhereClause != nil {
		p.skip("partial index", st.Idxname+" (skipped: predicate cannot be modeled)")
		return
	}

	var cols []struct {
		name string
		desc bool
	}
	for _, param := range st.IndexParams {
		elem := param.GetIndexElem()
		if elem == nil {
			continue
		}
		if elem.Expr != nil || elem.Name == "" {
			p.skip("expression index", st.Idxname+" (skipped: expression columns cannot be modeled)")
			return
		}
		cols = append(cols, struct {
			name string
			desc bool
		}{elem.Name, elem.Ordering == pg_query.SortByDir_SORTBY_DESC})
	}

	// Index targets resolve in a second pass alongside FKs, so that
	// CREATE INDEX may precede CREATE TABLE in the input.
	method := st.AccessMethod
	if method == "btree" {
		method = ""
	}
	p.pendingIndexes = append(p.pendingIndexes, pendingIndex{
		tableName: tableName,
		name:      st.Idxname,
		unique:    st.Unique,
		method:    method,
		columns:   cols,
	})
}

type pendingIndex struct {
	tableName string
	name      string
	unique    bool
	method    string
	columns   []struct {
		name string
		desc bool
	}
}

func (p *parseRun) alterTable(st *pg_query.AlterTableStmt) {
	name := p.tableName(st.Relation)
	table := p.schema.TableByName(name)
	for _, cmdNode := range st.Cmds {
		cmd := cmdNode.GetAlterTableCmd()
		if cmd == nil {
			continue
		}
		switch cmd.Subtype {
		case pg_query.AlterTableType_AT_AddConstraint:
			if table == nil {
				p.fail("ALTER TABLE %q: table not defined in this DDL", name)
				return
			}
			if c := cmd.GetDef().GetConstraint(); c != nil {
				p.constraint(table, c, nil)
			}
		case pg_query.AlterTableType_AT_AddColumn:
			if table == nil {
				p.fail("ALTER TABLE %q: table not defined in this DDL", name)
				return
			}
			if cd := cmd.GetDef().GetColumnDef(); cd != nil {
				p.column(table, cd)
			}
		default:
			p.skip("ALTER TABLE "+cmd.Subtype.String(), "on "+name)
		}
	}
}

// resolve turns name-based FK and index references into ID-based ones.
// From here on, the model never refers to anything by name.
func (p *parseRun) resolve() error {
	for _, fk := range p.pendingFKs {
		table := p.schema.TableByName(fk.tableName)
		if table == nil {
			return fmt.Errorf("internal: FK owner %q missing", fk.tableName)
		}
		ids, ok := p.columnIDs(table, fk.columns)
		if !ok {
			return p.err
		}
		target := p.schema.TableByName(fk.refTable)
		if target == nil {
			return fmt.Errorf("foreign key on %q references table %q, which is not defined in this DDL", fk.tableName, fk.refTable)
		}
		refCols := fk.refColumns
		if len(refCols) == 0 {
			// REFERENCES users — target defaults to the primary key.
			pk := target.PrimaryKey()
			if pk == nil {
				return fmt.Errorf("foreign key on %q references %q with no column list, but %q has no primary key", fk.tableName, fk.refTable, fk.refTable)
			}
			fk.constraint.RefColumnIDs = append([]schema.ObjectID(nil), pk.ColumnIDs...)
		} else {
			refIDs, ok := p.columnIDs(target, refCols)
			if !ok {
				return p.err
			}
			fk.constraint.RefColumnIDs = refIDs
		}
		fk.constraint.ColumnIDs = ids
		fk.constraint.RefTableID = target.ID
		table.Constraints = append(table.Constraints, fk.constraint)
	}

	for _, idx := range p.pendingIndexes {
		table := p.schema.TableByName(idx.tableName)
		if table == nil {
			return fmt.Errorf("index %q references table %q, which is not defined in this DDL", idx.name, idx.tableName)
		}
		ic := make([]schema.IndexColumn, 0, len(idx.columns))
		for _, c := range idx.columns {
			col := table.ColumnByName(c.name)
			if col == nil {
				return fmt.Errorf("index %q references column %q, which does not exist on %q", idx.name, c.name, idx.tableName)
			}
			ic = append(ic, schema.IndexColumn{ColumnID: col.ID, Desc: c.desc})
		}
		table.Indexes = append(table.Indexes, schema.Index{
			ID: schema.NewObjectID(), Name: idx.name, Columns: ic, Unique: idx.unique, Method: idx.method,
		})
	}

	return nil
}

func (p *parseRun) columnIDs(table *schema.Table, names []string) ([]schema.ObjectID, bool) {
	ids := make([]schema.ObjectID, 0, len(names))
	for _, n := range names {
		col := table.ColumnByName(n)
		if col == nil {
			p.fail("column %q does not exist on table %q", n, table.Name)
			return nil, false
		}
		ids = append(ids, col.ID)
	}
	return ids, true
}

// dataType normalizes a TypeName into the model's representation.
func (p *parseRun) dataType(tn *pg_query.TypeName, tableName, colName string) (schema.DataType, bool) {
	if tn == nil || len(tn.Names) == 0 {
		p.fail("column %s.%s has no type", tableName, colName)
		return schema.DataType{}, false
	}
	if len(tn.ArrayBounds) > 0 {
		p.skip("array type", tableName+"."+colName+" (imported as base type)")
	}
	base := strVal(tn.Names[len(tn.Names)-1])
	if alias, ok := typeAliases[base]; ok {
		base = alias
	}
	var params []int
	for _, tm := range tn.Typmods {
		if ac := tm.GetAConst(); ac != nil && ac.GetIval() != nil {
			params = append(params, int(ac.GetIval().Ival))
		}
	}
	return schema.DataType{Base: base, Params: params}, true
}

// typeAliases maps internal pg_catalog names to the names people write.
var typeAliases = map[string]string{
	"int2":    "smallint",
	"int4":    "integer",
	"int8":    "bigint",
	"bool":    "boolean",
	"bpchar":  "char",
	"float4":  "real",
	"float8":  "double precision",
	"timetz":  "time with time zone",
	"varbit":  "bit varying",
	"decimal": "numeric",
}

func refAction(code string) schema.ReferentialAction {
	switch code {
	case "r":
		return schema.Restrict
	case "c":
		return schema.Cascade
	case "n":
		return schema.SetNull
	case "d":
		return schema.SetDefault
	default: // "a" or unset
		return schema.NoAction
	}
}

func stringList(nodes []*pg_query.Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, strVal(n))
	}
	return out
}

func strVal(n *pg_query.Node) string {
	if s := n.GetString_(); s != nil {
		return s.Sval
	}
	return ""
}

// deparseExpr renders an expression node back to SQL by wrapping it in a
// minimal SELECT and stripping the prefix — pg_query deparses whole
// statements only.
func deparseExpr(n *pg_query.Node) (string, error) {
	if n == nil {
		return "", nil
	}
	wrapper := &pg_query.ParseResult{
		Stmts: []*pg_query.RawStmt{{
			Stmt: &pg_query.Node{Node: &pg_query.Node_SelectStmt{
				SelectStmt: &pg_query.SelectStmt{
					TargetList: []*pg_query.Node{{Node: &pg_query.Node_ResTarget{
						ResTarget: &pg_query.ResTarget{Val: n},
					}}},
					Op:          pg_query.SetOperation_SETOP_NONE,
					LimitOption: pg_query.LimitOption_LIMIT_OPTION_DEFAULT,
				},
			}},
		}},
	}
	out, err := pg_query.Deparse(wrapper)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(out, "SELECT "), nil
}
