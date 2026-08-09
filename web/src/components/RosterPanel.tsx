import { useEffect, useState } from "react";
import { api, type BulkRosterResult, type RosterEntry } from "../api";
import { parseRosterInput, toBulkEntries } from "../roster-parse";
import { Button, StatusChip, Empty, type Notify } from "./ui";

export function RosterPanel({ classroomID, notify }: { classroomID: string; notify: Notify }) {
  const [roster, setRoster] = useState<RosterEntry[] | null>(null);
  const [username, setUsername] = useState("");
  const [busy, setBusy] = useState(false);
  const [showBulk, setShowBulk] = useState(false);
  const [bulkText, setBulkText] = useState("");
  const [bulkResults, setBulkResults] = useState<BulkRosterResult[] | null>(null);

  async function load() {
    try {
      setRoster(await api.listRoster(classroomID));
    } catch (e) {
      notify(e instanceof Error ? e.message : String(e), "err");
    }
  }

  useEffect(() => {
    setRoster(null);
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [classroomID]);

  async function add() {
    const u = username.trim();
    if (!u) return;
    setBusy(true);
    try {
      await api.addRoster(classroomID, { username: u });
      setUsername("");
      notify(`Added ${u} to the roster`);
      void load();
    } catch (e) {
      notify(e instanceof Error ? e.message : String(e), "err");
    } finally {
      setBusy(false);
    }
  }

  async function addBulk() {
    const rows = parseRosterInput(bulkText);
    if (rows.length === 0) {
      notify("Nothing to add — paste one username per line", "err");
      return;
    }
    setBusy(true);
    try {
      // Any plaintext email is hashed here, in the browser; only the digest is sent.
      const entries = await toBulkEntries(rows, classroomID);
      const res = await api.addRosterBulk(classroomID, entries);
      setBulkResults(res.results);
      notify(
        `${res.created} added, ${res.already_present} already present, ${res.errors} error(s)`,
        res.errors > 0 ? "err" : "ok",
      );
      void load();
    } catch (e) {
      notify(e instanceof Error ? e.message : String(e), "err");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="section">
      <div className="section-head">
        <span className="section-title">
          Roster
          {roster && <span className="count">{roster.length}</span>}
        </span>
        <Button variant="ghost" small onClick={() => setShowBulk((v) => !v)}>
          {showBulk ? "Cancel" : "Bulk add"}
        </Button>
      </div>

      <div className="form-row" style={{ marginBottom: 14 }}>
        <input
          className="input"
          placeholder="Git username (e.g. octocat)"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") void add();
          }}
          style={{ minWidth: 240 }}
        />
        <Button variant="primary" disabled={busy} onClick={() => void add()}>
          Add student
        </Button>
        <span className="muted small">No names or emails — only the username is stored.</span>
      </div>

      {showBulk && (
        <div className="card" style={{ marginBottom: 14, padding: 14 }}>
          <textarea
            className="input"
            rows={8}
            placeholder={"One username per line, or username,email\n\noctocat\nhubot,student@example.edu"}
            value={bulkText}
            onChange={(e) => setBulkText(e.target.value)}
            style={{ width: "100%", fontFamily: "var(--mono, monospace)" }}
          />
          <div className="form-row" style={{ marginTop: 10 }}>
            <Button variant="primary" disabled={busy} onClick={() => void addBulk()}>
              {busy ? "Adding…" : "Add all"}
            </Button>
            <span className="muted small">
              Paste a roster or CSV export. Any email column is hashed in your browser — the
              address itself is never sent or stored. Re-pasting the same list adds no duplicates.
            </span>
          </div>

          {bulkResults && (
            <table className="table" style={{ marginTop: 12 }}>
              <thead>
                <tr>
                  <th>Username</th>
                  <th>Result</th>
                </tr>
              </thead>
              <tbody>
                {bulkResults.map((r, i) => (
                  <tr key={`${r.username}-${i}`}>
                    <td className="mono">{r.username || "—"}</td>
                    <td>
                      {r.status === "error" ? (
                        <span className="chip chip-danger" title={r.error}>
                          error: {r.error}
                        </span>
                      ) : r.status === "created" ? (
                        <span className="chip chip-ok">added</span>
                      ) : (
                        <span className="chip chip-warn">already present</span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}

      {roster === null ? (
        <p className="muted small">Loading roster…</p>
      ) : roster.length === 0 ? (
        <Empty>No students invited yet.</Empty>
      ) : (
        <table className="table">
          <thead>
            <tr>
              <th>Username</th>
              <th>Status</th>
              <th>Claimed</th>
            </tr>
          </thead>
          <tbody>
            {roster.map((r) => (
              <tr key={r.id}>
                <td className="mono">{r.host_username}</td>
                <td>
                  <StatusChip status={r.status} />
                </td>
                <td className="mono muted">{r.claimed_at ? new Date(r.claimed_at).toLocaleString() : "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
