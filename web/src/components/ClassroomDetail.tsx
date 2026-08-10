import { useEffect, useState } from "react";
import { api, type Assignment, type Classroom } from "../api";
import { Button, Field, Empty, type Notify } from "./ui";
import { AssignmentCard } from "./AssignmentCard";
import { RosterPanel } from "./RosterPanel";

export function ClassroomDetail({ classroom, notify }: { classroom: Classroom; notify: Notify }) {
  const [assignments, setAssignments] = useState<Assignment[] | null>(null);
  const [showForm, setShowForm] = useState(false);
  const [confirmingExport, setConfirmingExport] = useState(false);

  async function confirmExport() {
    if (
      !window.confirm(
        "This starts the retention countdown for every currently graded submission in this classroom. " +
          "It does not delete anything now, and does not re-download grades.csv — only download and confirm once you've " +
          "actually gotten the grades into your LMS. Continue?",
      )
    ) {
      return;
    }
    setConfirmingExport(true);
    try {
      const res = await api.confirmExport(classroom.id);
      notify(`Export confirmed for ${res.confirmed} grade(s) — retention countdown started`);
    } catch (e) {
      notify(e instanceof Error ? e.message : String(e), "err");
    } finally {
      setConfirmingExport(false);
    }
  }

  async function load() {
    try {
      setAssignments(await api.listAssignments(classroom.id));
    } catch (e) {
      notify(e instanceof Error ? e.message : String(e), "err");
    }
  }

  useEffect(() => {
    setAssignments(null);
    setShowForm(false);
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [classroom.id]);

  return (
    <div className="fade-in">
      <header className="page-head">
        <div>
          <h1 className="page-title">{classroom.name}</h1>
          <div className="page-meta">
            <span>{classroom.host}</span>
            <span className="dot">/</span>
            <span>{classroom.host_namespace}</span>
            <span className="dot">·</span>
            <span>id {classroom.id.slice(0, 8)}</span>
          </div>
        </div>
        <div className="form-row">
          <a className="btn" href={api.gradesCsvUrl(classroom.id)} download>
            ↓ grades.csv
          </a>
          <Button variant="ghost" small disabled={confirmingExport} onClick={() => void confirmExport()}>
            {confirmingExport ? "Confirming…" : "Confirm export"}
          </Button>
        </div>
      </header>

      <div className="section">
        <div className="section-head">
          <span className="section-title">
            Assignments
            {assignments && <span className="count">{assignments.length}</span>}
          </span>
          <Button variant="ghost" small onClick={() => setShowForm((v) => !v)}>
            {showForm ? "Cancel" : "+ New assignment"}
          </Button>
        </div>

        {showForm && (
          <NewAssignmentForm
            classroom={classroom}
            notify={notify}
            onCreated={() => {
              setShowForm(false);
              void load();
            }}
          />
        )}

        {assignments === null ? (
          <p className="muted small">Loading assignments…</p>
        ) : assignments.length === 0 ? (
          <Empty>No assignments yet. Create one from a template repository.</Empty>
        ) : (
          assignments.map((a, i) => (
            <div key={a.id} className="fade-in" style={{ animationDelay: `${i * 45}ms` }}>
              <AssignmentCard assignment={a} notify={notify} joinPolicy={classroom.join_policy} />
            </div>
          ))
        )}
      </div>

      <RosterPanel classroomID={classroom.id} notify={notify} />
    </div>
  );
}

function NewAssignmentForm({
  classroom,
  notify,
  onCreated,
}: {
  classroom: Classroom;
  notify: Notify;
  onCreated: () => void;
}) {
  const [title, setTitle] = useState("");
  const [slug, setSlug] = useState("");
  const [namespace, setNamespace] = useState(classroom.host_namespace);
  const [name, setName] = useState("");
  const [type, setType] = useState("individual");
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!slug.trim() || !namespace.trim() || !name.trim()) {
      notify("slug, template namespace, and template name are required", "err");
      return;
    }
    setBusy(true);
    try {
      await api.createAssignment(classroom.id, {
        title: title.trim() || slug.trim(),
        slug: slug.trim(),
        template: { namespace: namespace.trim(), name: name.trim() },
        type,
      });
      notify(`Created assignment ${slug.trim()}`);
      onCreated();
    } catch (e) {
      notify(e instanceof Error ? e.message : String(e), "err");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="inline-form">
      <div className="form-grid">
        <Field label="Title">
          <input className="input" value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Homework 1" />
        </Field>
        <Field label="Slug">
          <input className="input" value={slug} onChange={(e) => setSlug(e.target.value)} placeholder="hw1" />
        </Field>
        <Field label="Template namespace">
          <input className="input" value={namespace} onChange={(e) => setNamespace(e.target.value)} placeholder="cs101-org" />
        </Field>
        <Field label="Template repo">
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="hw1-template" />
        </Field>
        <Field label="Type">
          <select className="select" value={type} onChange={(e) => setType(e.target.value)}>
            <option value="individual">individual</option>
            <option value="group">group</option>
          </select>
        </Field>
      </div>
      <div className="form-actions">
        <Button variant="primary" disabled={busy} onClick={() => void submit()}>
          Create assignment
        </Button>
      </div>
    </div>
  );
}
