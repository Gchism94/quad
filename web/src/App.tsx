import { useEffect, useMemo, useState } from "react";
import { api, type Classroom, type Operator } from "./api";
import { Sidebar } from "./components/Sidebar";
import { ClassroomDetail } from "./components/ClassroomDetail";
import { Button, Field, Modal, type Notify } from "./components/ui";

interface Toast {
  msg: string;
  kind: "ok" | "err";
}

export default function App() {
  const [operator, setOperator] = useState<Operator | null | undefined>(undefined);
  const [classrooms, setClassrooms] = useState<Classroom[]>([]);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [toast, setToast] = useState<Toast | null>(null);

  const notify: Notify = (msg, kind = "ok") => {
    setToast({ msg, kind });
    window.setTimeout(() => setToast(null), 3600);
  };

  async function refresh(selectFirst = false) {
    try {
      const cs = await api.listClassrooms();
      setClassrooms(cs);
      if ((selectFirst || selectedId === null) && cs.length > 0) {
        setSelectedId((cur) => cur ?? cs[0].id);
      }
    } catch (e) {
      notify(e instanceof Error ? e.message : String(e), "err");
    }
  }

  // Resolve the current operator once.
  useEffect(() => {
    api.me()
      .then((m) => setOperator(m))
      .catch(() => setOperator(null));
  }, []);

  // Load classrooms only after we know an operator is signed in.
  useEffect(() => {
    if (operator) void refresh(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [operator]);

  async function handleLogout() {
    try {
      await api.logout();
    } catch {
      /* ignore */
    }
    window.location.reload();
  }

  const selected = useMemo(
    () => classrooms.find((c) => c.id === selectedId) ?? null,
    [classrooms, selectedId],
  );

  if (operator === undefined) {
    return <div className="splash mono">loading…</div>;
  }
  if (operator === null) {
    return (
      <div className="login">
        <div className="login-card">
          <span className="brand-mark">cairn</span>
          <h1>Instructor console</h1>
          <p className="muted">Sign in with your Git host account to continue.</p>
          <Button variant="primary" onClick={() => (window.location.href = api.loginUrl())}>
            Sign in with GitHub
          </Button>
          <p className="small muted login-note">
            Access is limited to operators allowlisted on this instance.
          </p>
          <p className="small muted login-note">
            Returning student? <a href="/student/login">Sign in here</a>.
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="app">
      <Sidebar
        classrooms={classrooms}
        selectedId={selectedId}
        onSelect={setSelectedId}
        onNew={() => setShowNew(true)}
        operator={operator}
        onLogout={handleLogout}
      />

      <main className="main">
        {selected ? (
          <ClassroomDetail key={selected.id} classroom={selected} notify={notify} />
        ) : (
          <div className="landing">
            <h1>Welcome to your instructor console</h1>
            <p className="muted">
              Create a classroom backed by a Git-host organization to get started.
            </p>
            <Button variant="primary" onClick={() => setShowNew(true)}>
              + New classroom
            </Button>
          </div>
        )}
      </main>

      {showNew && (
        <NewClassroomModal
          notify={notify}
          onClose={() => setShowNew(false)}
          onCreated={(c) => {
            setShowNew(false);
            setSelectedId(c.id);
            void refresh();
          }}
        />
      )}

      {toast && (
        <div className={"toast" + (toast.kind === "err" ? " is-err" : "")}>
          <div>
            <div className="mono">{toast.kind === "err" ? "error" : "ok"}</div>
            {toast.msg}
          </div>
        </div>
      )}
    </div>
  );
}

function NewClassroomModal({
  notify,
  onClose,
  onCreated,
}: {
  notify: Notify;
  onClose: () => void;
  onCreated: (c: Classroom) => void;
}) {
  const [name, setName] = useState("");
  const [host, setHost] = useState("github");
  const [namespace, setNamespace] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit() {
    if (!name.trim() || !namespace.trim()) {
      notify("name and host organization are required", "err");
      return;
    }
    setBusy(true);
    try {
      const c = await api.createClassroom({
        name: name.trim(),
        host,
        host_namespace: namespace.trim(),
      });
      notify(`Created classroom ${c.name}`);
      onCreated(c);
    } catch (e) {
      notify(e instanceof Error ? e.message : String(e), "err");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title="New classroom"
      subtitle="Backed by an existing organization on your Git host."
      onClose={onClose}
    >
      <div className="form-grid">
        <Field label="Course name" full>
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="CS101 · Fall 2025" />
        </Field>
        <Field label="Host">
          <select className="select" value={host} onChange={(e) => setHost(e.target.value)}>
            <option value="github">github</option>
            <option value="gitlab">gitlab</option>
            <option value="forgejo">forgejo</option>
          </select>
        </Field>
        <Field label="Host organization">
          <input className="input" value={namespace} onChange={(e) => setNamespace(e.target.value)} placeholder="cs101-fall25" />
        </Field>
      </div>
      <div className="form-actions">
        <Button variant="ghost" onClick={onClose}>
          Cancel
        </Button>
        <Button variant="primary" disabled={busy} onClick={() => void submit()}>
          Create
        </Button>
      </div>
    </Modal>
  );
}
