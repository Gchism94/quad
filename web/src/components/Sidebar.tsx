import type { Classroom, Operator } from "../api";
import { Button } from "./ui";

export function Sidebar({
  classrooms,
  selectedId,
  onSelect,
  onNew,
  operator,
  onLogout,
}: {
  classrooms: Classroom[];
  selectedId: string | null;
  onSelect: (id: string) => void;
  onNew: () => void;
  operator?: Operator;
  onLogout?: () => void;
}) {
  return (
    <aside className="sidebar">
      <div className="brand">
        <span className="brand-mark">cairn</span>
        <span className="brand-sub">instructor console</span>
      </div>

      <div className="nav-head">
        <span>Classrooms</span>
        <Button variant="ghost" small onClick={onNew}>
          + New
        </Button>
      </div>

      <nav className="nav">
        {classrooms.length === 0 && <p className="muted small">No classrooms yet.</p>}
        {classrooms.map((c) => (
          <button
            key={c.id}
            className={"nav-item" + (c.id === selectedId ? " is-active" : "")}
            onClick={() => onSelect(c.id)}
          >
            <span className="nav-item-name">{c.name}</span>
            <span className="nav-item-meta">
              {c.host}/{c.host_namespace}
            </span>
          </button>
        ))}
      </nav>

      <div className="sidebar-foot">
        {operator && operator.auth === "enabled" ? (
          <div className="op-row">
            <span className="op-name mono" title="Signed-in operator">
              @{operator.username}
            </span>
            {onLogout && (
              <Button variant="ghost" small onClick={onLogout}>
                Sign out
              </Button>
            )}
          </div>
        ) : (
          <span className="small muted">auth disabled</span>
        )}
        <span className="small muted">AGPL · self-hosted</span>
      </div>
    </aside>
  );
}
