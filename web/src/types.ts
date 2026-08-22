// Mirrors the Go model and API payloads.

export type ObjectID = string;

export interface DataType {
  base: string;
  params?: number[];
}

export interface Column {
  id: ObjectID;
  name: string;
  type: DataType;
  nullable: boolean;
  default?: string;
  position: number;
}

export type ConstraintKind = "primary_key" | "foreign_key" | "unique" | "check";

export interface Constraint {
  id: ObjectID;
  name?: string;
  kind: ConstraintKind;
  column_ids?: ObjectID[];
  ref_table_id?: ObjectID;
  ref_column_ids?: ObjectID[];
  on_delete?: string;
  on_update?: string;
  expr?: string;
}

export interface IndexColumn {
  column_id: ObjectID;
  desc?: boolean;
}

export interface Index {
  id: ObjectID;
  name: string;
  columns: IndexColumn[];
  unique?: boolean;
  method?: string;
}

export interface Table {
  id: ObjectID;
  name: string;
  columns: Column[];
  constraints?: Constraint[];
  indexes?: Index[];
}

export interface Schema {
  tables: Table[];
}

export interface Project {
  id: string;
  name: string;
  created_at: string;
}

export interface Branch {
  id: string;
  project_id: string;
  name: string;
  head_commit_id: string;
  created_at: string;
}

export interface CommitMeta {
  id: string;
  message: string;
  author: string;
  parent_id?: string;
  parent2_id?: string;
  created_at: string;
  tables: number;
}

export interface Unsupported {
  construct: string;
  detail: string;
}

export interface ApiError {
  code: string;
  message: string;
  hint?: string;
}

export interface Change {
  kind: string;
  table: string;
  table_id: ObjectID;
  object?: string;
  object_id?: ObjectID;
  from?: string;
  to?: string;
  text: string;
}

export interface Conflict {
  id: string;
  class: string;
  table: string;
  object?: string;
  property: string;
  base: string;
  ours: string;
  theirs: string;
  description: string;
  allow_custom: boolean;
  custom_kind?: string;
}

export interface Resolution {
  conflict_id: string;
  choice: "ours" | "theirs" | "custom";
  custom?: string;
}

export interface Problem {
  code: string;
  message: string;
  table?: string;
  object?: string;
}

export interface MigrationStatement {
  phase: string;
  sql: string;
}

export interface MigrationWarning {
  code: string;
  message: string;
  sql: string;
}

export interface GraphCommit {
  id: string;
  message: string;
  author: string;
  parents: string[];
  created_at: string;
  is_merge: boolean;
  heads?: string[];
}

export interface Proposal {
  kind: "table" | "column";
  table?: string;
  old_id: ObjectID;
  old_name: string;
  new_id: ObjectID;
  new_name: string;
  confidence: number;
  detail: string;
}

// Op mirrors internal/ops.Op — only the fields for its kind are set.
export interface Op {
  op: string;
  table_id?: ObjectID;
  column_id?: ObjectID;
  constraint_id?: ObjectID;
  index_id?: ObjectID;
  name?: string;
  column?: { name: string; type: DataType; nullable: boolean; default?: string };
  columns?: { name: string; type: DataType; nullable: boolean; default?: string }[];
  type?: DataType;
  nullable?: boolean;
  default?: string;
  constraint?: {
    kind: ConstraintKind;
    column_ids?: ObjectID[];
    ref_table_id?: ObjectID;
    ref_column_ids?: ObjectID[];
    expr?: string;
  };
  index?: { name: string; columns: IndexColumn[]; unique?: boolean; method?: string };
}

export function typeString(t: DataType): string {
  if (!t.params || t.params.length === 0) return t.base;
  return `${t.base}(${t.params.join(",")})`;
}
