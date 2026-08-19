import type { ApiError, Branch, CommitMeta, Project, Schema, Unsupported } from "./types";

// RequestError carries the server's {code, message, hint} envelope so screens
// can show the hint, not just a status code.
export class RequestError extends Error {
  code: string;
  hint?: string;
  status: number;
  constructor(status: number, err: ApiError) {
    super(err.message);
    this.status = status;
    this.code = err.code;
    this.hint = err.hint;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const resp = await fetch(path, {
    headers: init?.body ? { "Content-Type": "application/json" } : undefined,
    ...init,
  });
  const body = await resp.json().catch(() => null);
  if (!resp.ok) {
    const err: ApiError = body?.error ?? {
      code: "unknown",
      message: `Request failed with status ${resp.status}.`,
      hint: "Try again; if it persists the server may be restarting.",
    };
    throw new RequestError(resp.status, err);
  }
  return body as T;
}

export const api = {
  listProjects: () => request<{ projects: Project[] }>("/api/projects"),

  createProject: (name: string, ddl: string, author: string) =>
    request<{ project: Project; branch: Branch; commit_id: string; unsupported: Unsupported[] }>(
      "/api/projects",
      { method: "POST", body: JSON.stringify({ name, ddl, author }) },
    ),

  getProject: (id: string) => request<{ project: Project; branches: Branch[] }>(`/api/projects/${id}`),

  createBranch: (projectId: string, name: string, from: string) =>
    request<{ branch: Branch }>(`/api/projects/${projectId}/branches`, {
      method: "POST",
      body: JSON.stringify({ name, from }),
    }),

  branchSchema: (branchId: string) =>
    request<{ branch: Branch; schema: Schema; unsupported: Unsupported[] | null }>(
      `/api/branches/${branchId}/schema`,
    ),

  branchCommits: (branchId: string) => request<{ commits: CommitMeta[] }>(`/api/branches/${branchId}/commits`),

  demoReset: () => request<{ status: string }>("/api/demo/reset", { method: "POST" }),
};
