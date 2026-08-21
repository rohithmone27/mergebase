// Package seed builds the demo workspace a first-time visitor lands in: a
// realistic payments schema with two diverged branches and one prepared
// conflict, so the interesting state is one click away instead of behind an
// empty screen.
//
// Divergence is produced by mutating cloned snapshots directly — clones
// preserve ObjectIDs, so both branches agree on the identity of every shared
// object exactly as real edits through the app would.
package seed

import (
	"fmt"

	"github.com/rohithmone27/mergebase/internal/parser"
	"github.com/rohithmone27/mergebase/internal/schema"
	"github.com/rohithmone27/mergebase/internal/store"
)

const baseDDL = `
CREATE TABLE users (
    id         BIGSERIAL PRIMARY KEY,
    email      VARCHAR(255) NOT NULL,
    name       TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT now()
);

CREATE TABLE orders (
    id       BIGSERIAL PRIMARY KEY,
    user_id  BIGINT NOT NULL REFERENCES users (id),
    status   VARCHAR(16) NOT NULL DEFAULT 'pending',
    total    NUMERIC(12,2) NOT NULL,
    CHECK (total >= 0)
);

CREATE TABLE payments (
    id        BIGSERIAL PRIMARY KEY,
    order_id  BIGINT NOT NULL REFERENCES orders (id),
    amount    NUMERIC(12,2) NOT NULL,
    method    VARCHAR(20) NOT NULL
);

CREATE INDEX idx_orders_status ON orders (status);
`

// Ensure seeds the demo workspace if the store is empty. Calling it on a
// populated store is a no-op, so restarts never disturb user-created data.
func Ensure(s *store.Store) error {
	projects, err := s.ListProjects()
	if err != nil {
		return err
	}
	if len(projects) > 0 {
		return nil
	}

	res, err := parser.Parse(baseDDL)
	if err != nil {
		return fmt.Errorf("seed DDL failed to parse: %w", err)
	}
	base := res.Schema

	project, err := s.CreateProject("Payments Platform")
	if err != nil {
		return err
	}

	// main: c1 (import) → c2 (refunds table) → c3 (widen email).
	c1 := &store.Commit{ProjectID: project.ID, Message: "Initial schema import", Author: "alice", Schema: base}
	if err := s.CreateCommit(c1); err != nil {
		return err
	}
	main, err := s.CreateBranch(project.ID, "main", c1.ID)
	if err != nil {
		return err
	}

	s2 := base.Clone()
	addRefunds(s2)
	c2 := &store.Commit{ProjectID: project.ID, Message: "Add refunds table", Author: "alice", ParentID: c1.ID, Schema: s2}
	if err := s.CommitAndMoveHead(main.ID, c1.ID, c2); err != nil {
		return err
	}

	s3 := s2.Clone()
	s3.TableByName("users").ColumnByName("email").Type = schema.DataType{Base: "varchar", Params: []int{500}}
	c3 := &store.Commit{ProjectID: project.ID, Message: "Widen users.email to varchar(500)", Author: "alice", ParentID: c2.ID, Schema: s3}
	if err := s.CommitAndMoveHead(main.ID, c2.ID, c3); err != nil {
		return err
	}

	// feature/billing branches from c1: b1 (invoices) → b2 (email TEXT + rename).
	billing, err := s.CreateBranch(project.ID, "feature/billing", c1.ID)
	if err != nil {
		return err
	}

	t1 := base.Clone()
	addInvoices(t1)
	b1 := &store.Commit{ProjectID: project.ID, Message: "Add invoices table with order index", Author: "rohith", ParentID: c1.ID, Schema: t1}
	if err := s.CommitAndMoveHead(billing.ID, c1.ID, b1); err != nil {
		return err
	}

	t2 := t1.Clone()
	users := t2.TableByName("users")
	// Retype email → TEXT: together with main's varchar(500) this is the
	// prepared C2 conflict (both sides changed the same property differently).
	users.ColumnByName("email").Type = schema.DataType{Base: "text"}
	// Rename name → full_name: same ID, new name — merges cleanly against
	// main and shows rename-awareness in the diff.
	users.ColumnByName("name").Name = "full_name"
	b2 := &store.Commit{ProjectID: project.ID, Message: "Email as TEXT; rename name to full_name", Author: "rohith", ParentID: b1.ID, Schema: t2}
	if err := s.CommitAndMoveHead(billing.ID, b1.ID, b2); err != nil {
		return err
	}

	return s.AppendEvent(project.ID, "", "seed", map[string]string{"workspace": "demo"})
}

func addRefunds(s *schema.Schema) {
	payments := s.TableByName("payments")
	id := schema.Column{ID: schema.NewObjectID(), Name: "id", Type: schema.DataType{Base: "bigserial"}, Nullable: false, Position: 1}
	paymentID := schema.Column{ID: schema.NewObjectID(), Name: "payment_id", Type: schema.DataType{Base: "bigint"}, Nullable: false, Position: 2}
	amount := schema.Column{ID: schema.NewObjectID(), Name: "amount", Type: schema.DataType{Base: "numeric", Params: []int{12, 2}}, Nullable: false, Position: 3}
	reason := schema.Column{ID: schema.NewObjectID(), Name: "reason", Type: schema.DataType{Base: "text"}, Nullable: true, Position: 4}
	s.Tables = append(s.Tables, schema.Table{
		ID:      schema.NewObjectID(),
		Name:    "refunds",
		Columns: []schema.Column{id, paymentID, amount, reason},
		Constraints: []schema.Constraint{
			{ID: schema.NewObjectID(), Kind: schema.PrimaryKey, ColumnIDs: []schema.ObjectID{id.ID}},
			{ID: schema.NewObjectID(), Kind: schema.ForeignKey, ColumnIDs: []schema.ObjectID{paymentID.ID},
				RefTableID: payments.ID, RefColumnIDs: []schema.ObjectID{payments.ColumnByName("id").ID},
				OnDelete: schema.NoAction, OnUpdate: schema.NoAction},
		},
	})
}

func addInvoices(s *schema.Schema) {
	orders := s.TableByName("orders")
	id := schema.Column{ID: schema.NewObjectID(), Name: "id", Type: schema.DataType{Base: "bigserial"}, Nullable: false, Position: 1}
	orderID := schema.Column{ID: schema.NewObjectID(), Name: "order_id", Type: schema.DataType{Base: "bigint"}, Nullable: false, Position: 2}
	number := schema.Column{ID: schema.NewObjectID(), Name: "number", Type: schema.DataType{Base: "varchar", Params: []int{32}}, Nullable: false, Position: 3}
	issuedAt := schema.Column{ID: schema.NewObjectID(), Name: "issued_at", Type: schema.DataType{Base: "timestamp"}, Nullable: false, Position: 4}
	table := schema.Table{
		ID:      schema.NewObjectID(),
		Name:    "invoices",
		Columns: []schema.Column{id, orderID, number, issuedAt},
		Constraints: []schema.Constraint{
			{ID: schema.NewObjectID(), Kind: schema.PrimaryKey, ColumnIDs: []schema.ObjectID{id.ID}},
			{ID: schema.NewObjectID(), Kind: schema.Unique, ColumnIDs: []schema.ObjectID{number.ID}},
			{ID: schema.NewObjectID(), Kind: schema.ForeignKey, ColumnIDs: []schema.ObjectID{orderID.ID},
				RefTableID: orders.ID, RefColumnIDs: []schema.ObjectID{orders.ColumnByName("id").ID},
				OnDelete: schema.NoAction, OnUpdate: schema.NoAction},
		},
	}
	table.Indexes = []schema.Index{
		{ID: schema.NewObjectID(), Name: "idx_invoices_order", Columns: []schema.IndexColumn{{ColumnID: orderID.ID}}},
	}
	s.Tables = append(s.Tables, table)
}
