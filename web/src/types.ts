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

export function typeString(t: DataType): string {
  if (!t.params || t.params.length === 0) return t.base;
  return `${t.base}(${t.params.join(",")})`;
}
