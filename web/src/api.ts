// Typed client for the Cairn control-plane API. Field names match the server's
// snake_case JSON. The base URL is empty by default (same-origin); in dev the
// Vite proxy forwards API prefixes to the Go server.

export type Host = string;

export interface Classroom {
  id: string;
  name: string;
  host: Host;
  host_namespace: string;
  // "open" (any authenticated student on the host may self-enroll) or "roster"
  // (only usernames already on the roster). Decides what the invite link's
  // accompanying note tells the instructor.
  join_policy: string;
  created_by: string;
  created_at: string;
}

export interface TemplateRef {
  host: Host;
  namespace: string;
  name: string;
  ref?: string;
}

export interface Assignment {
  id: string;
  classroom_id: string;
  title: string;
  slug: string;
  template: TemplateRef;
  type: string;
  deadline?: string;
  grading_spec: string;
  created_at: string;
}

export interface RosterEntry {
  id: string;
  classroom_id: string;
  host: Host;
  host_username: string;
  email_hash?: string;
  status: string;
  claimed_at?: string;
}

export interface RepoRef {
  host: Host;
  namespace: string;
  name: string;
}

export interface SubmissionView {
  id: string;
  roster_entry_id: string;
  username: string;
  repo: RepoRef;
  status: string;
  score?: number;
  max_score?: number;
  graded_at?: string;
}

export interface EnqueueResult {
  status: string;
  jobs_enqueued: number;
}

export interface Operator {
  id?: string;
  username: string;
  auth: "enabled" | "disabled";
}

const BASE = (import.meta.env.VITE_API_BASE as string | undefined) ?? "";

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(BASE + path, {
    method,
    credentials: "same-origin",
    headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`;
    try {
      const parsed = JSON.parse(text) as { error?: string };
      if (parsed.error) message = parsed.error;
    } catch {
      /* non-JSON error body */
    }
    throw new Error(message);
  }
  return (text ? (JSON.parse(text) as T) : (undefined as T));
}

export const api = {
  health: () => req<{ status: string }>("GET", "/healthz"),

  me: () => req<Operator>("GET", "/auth/me"),
  logout: () => req<{ status: string }>("POST", "/auth/logout"),
  loginUrl: () => `${BASE}/auth/login`,

  listClassrooms: () => req<Classroom[]>("GET", "/classrooms"),
  createClassroom: (b: { name: string; host: string; host_namespace: string }) =>
    req<Classroom>("POST", "/classrooms", b),

  listAssignments: (classroomID: string) =>
    req<Assignment[]>("GET", `/classrooms/${classroomID}/assignments`),
  createAssignment: (
    classroomID: string,
    b: {
      title: string;
      slug: string;
      template: { namespace: string; name: string; ref?: string };
      type?: string;
      grading_spec?: string;
      deadline?: string;
    },
  ) => req<Assignment>("POST", `/classrooms/${classroomID}/assignments`, b),

  listRoster: (classroomID: string) =>
    req<RosterEntry[]>("GET", `/classrooms/${classroomID}/roster`),
  addRoster: (classroomID: string, b: { username: string; email_hash?: string }) =>
    req<RosterEntry>("POST", `/classrooms/${classroomID}/roster`, b),

  listSubmissions: (assignmentID: string) =>
    req<SubmissionView[]>("GET", `/assignments/${assignmentID}/submissions`),

  setDeadline: (assignmentID: string, deadline: string | null) =>
    req<Assignment>("PATCH", `/assignments/${assignmentID}/deadline`, { deadline }),

  lock: (assignmentID: string) => req<EnqueueResult>("POST", `/assignments/${assignmentID}/lock`),
  unlock: (assignmentID: string) => req<EnqueueResult>("POST", `/assignments/${assignmentID}/unlock`),
  grade: (assignmentID: string) => req<EnqueueResult>("POST", `/assignments/${assignmentID}/grade`),

  gradesCsvUrl: (classroomID: string) => `${BASE}/classrooms/${classroomID}/grades.csv`,

  // The student-facing join URL for an assignment. Unlike gradesCsvUrl this is
  // absolute: it is copied and pasted into an LMS or email, so a same-origin
  // relative path would be useless to the student receiving it.
  inviteUrl: (assignmentID: string) =>
    `${window.location.origin}${BASE}/assignments/${assignmentID}/accept`,
};
