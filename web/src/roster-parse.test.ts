import { describe, expect, it } from "vitest";
import { parseRosterInput, toBulkEntries } from "./roster-parse";

// These cases were originally run by hand during CC-CA6 (compiled with tsc and
// executed against the emitted JS) because this project had no test runner.
// They are checked in here so a regression fails a command instead of waiting
// for the next person to happen to re-verify by hand.

describe("parseRosterInput", () => {
  it("reads one username per line", () => {
    expect(parseRosterInput("octocat\nhubot\n")).toEqual([
      { username: "octocat" },
      { username: "hubot" },
    ]);
  });

  it("skips blank lines", () => {
    expect(parseRosterInput("octocat\n\n\nhubot")).toEqual([
      { username: "octocat" },
      { username: "hubot" },
    ]);
  });

  it("handles CRLF line endings", () => {
    // A roster pasted from Windows, or saved by Excel, arrives with \r\n.
    expect(parseRosterInput("octocat\r\nhubot")).toEqual([
      { username: "octocat" },
      { username: "hubot" },
    ]);
  });

  it("reads a username,email CSV pair", () => {
    expect(parseRosterInput("octocat,student@example.edu")).toEqual([
      { username: "octocat", email: "student@example.edu" },
    ]);
  });

  it("treats a 64-char hex second column as an already-computed digest", () => {
    const digest = "a".repeat(64);
    expect(parseRosterInput(`octocat,${digest}`)).toEqual([
      { username: "octocat", emailHash: digest },
    ]);
  });

  it("treats a non-hex second column as a plaintext address, not a digest", () => {
    // 63 chars — one short of a digest, so it must not be mistaken for one.
    const notADigest = "a".repeat(63);
    expect(parseRosterInput(`octocat,${notADigest}`)).toEqual([
      { username: "octocat", email: notADigest },
    ]);
  });

  it("ignores a header row from an LMS export", () => {
    expect(parseRosterInput("username,email\noctocat,s@e.edu")).toEqual([
      { username: "octocat", email: "s@e.edu" },
    ]);
  });

  it("keeps a bare 'username' line, which has no second column", () => {
    // Only a *header* is dropped. A lone "username" is an odd but legal
    // username and must survive.
    expect(parseRosterInput("username")).toEqual([{ username: "username" }]);
  });

  it("strips surrounding quotes and whitespace", () => {
    expect(parseRosterInput('  "octocat" , "s@e.edu" ')).toEqual([
      { username: "octocat", email: "s@e.edu" },
    ]);
  });

  it("ignores extra columns rather than rejecting the paste", () => {
    // Real LMS exports carry name, section, role, and more. Failing the whole
    // paste over them would be hostile.
    expect(parseRosterInput("octocat,s@e.edu,Smith,John,99")).toEqual([
      { username: "octocat", email: "s@e.edu" },
    ]);
  });

  it("returns nothing for empty or whitespace-only input", () => {
    expect(parseRosterInput("")).toEqual([]);
    expect(parseRosterInput("   \n\n  ")).toEqual([]);
  });
});

describe("toBulkEntries", () => {
  const SALT = "classroom-1";

  it("never lets a plaintext address reach the payload", async () => {
    // The privacy property this module exists to enforce. RosterEntry is
    // documented as never carrying a plaintext email, so the address must not
    // survive anywhere in the serialized request body.
    const rows = parseRosterInput("octocat,student@example.edu");
    const entries = await toBulkEntries(rows, SALT);

    expect(JSON.stringify(entries)).not.toContain("student@example.edu");
    expect(JSON.stringify(entries)).not.toContain("student");
    expect(entries[0].email_hash).toMatch(/^[0-9a-f]{64}$/);
  });

  it("hashes an address to a SHA-256 hex digest", async () => {
    const [entry] = await toBulkEntries([{ username: "x", email: "s@e.edu" }], SALT);
    expect(entry.email_hash).toMatch(/^[0-9a-f]{64}$/);
  });

  it("salts the digest per classroom", async () => {
    // The same student in two courses must not produce the same digest, or the
    // hash becomes a cross-classroom identifier.
    const [a] = await toBulkEntries([{ username: "x", email: "s@e.edu" }], "c1");
    const [b] = await toBulkEntries([{ username: "x", email: "s@e.edu" }], "c2");
    expect(a.email_hash).not.toBe(b.email_hash);
  });

  it("hashes case-insensitively", async () => {
    const [lower] = await toBulkEntries([{ username: "x", email: "s@e.edu" }], SALT);
    const [upper] = await toBulkEntries([{ username: "x", email: "S@E.EDU" }], SALT);
    expect(upper.email_hash).toBe(lower.email_hash);
  });

  it("passes a precomputed digest through untouched", async () => {
    const digest = "b".repeat(64);
    const [entry] = await toBulkEntries([{ username: "x", emailHash: digest }], SALT);
    expect(entry.email_hash).toBe(digest);
  });

  it("sends no email_hash at all for a username-only row", async () => {
    // Not a hash of the empty string — the field is simply absent.
    const [entry] = await toBulkEntries([{ username: "x" }], SALT);
    expect(entry).toEqual({ username: "x" });
    expect(entry.email_hash).toBeUndefined();
  });

  it("drops the address rather than sending it when SubtleCrypto is unavailable", async () => {
    // Outside a secure context (plain http on a non-localhost host) there is no
    // crypto.subtle. Losing an optional matching hint is strictly better than
    // leaking the address, so the entry must go out with no email_hash — and
    // above all without the plaintext.
    const realCrypto = globalThis.crypto;
    // @ts-expect-error — deliberately simulating an insecure context.
    delete globalThis.crypto;
    try {
      const entries = await toBulkEntries([{ username: "x", email: "s@e.edu" }], SALT);
      expect(entries).toEqual([{ username: "x" }]);
      expect(JSON.stringify(entries)).not.toContain("s@e.edu");
    } finally {
      globalThis.crypto = realCrypto;
    }
  });

  it("produces the digests the Go agent produces for the same input", async () => {
    // internal/rosteragent hashes identically (sha256(salt + ':' + lowercased
    // address)) so a roster entered partly by hand here and partly by
    // `cairn roster pull` yields one digest per student, not two. These vectors
    // are asserted on the Go side too, in TestHashEmailMatchesDashboardFormula.
    const vectors: Array<[string, string, string]> = [
      ["c1", "jane@example.edu", "73e06298d3d651919c30c0d078e0bb8062b8250cf47553bdcbe822f90e44edfc"],
      ["c2", "jane@example.edu", "db8501dd277c4f2b0bde0068786a83a96cd15b37a3c1e49e20bbd68d407a5276"],
      ["abc-123", "bob.smith@arizona.edu", "923469e646c0b6b3f66f372f23d94e595c07efe1f6172d0e80b645d824fd9370"],
    ];
    for (const [salt, email, want] of vectors) {
      const [entry] = await toBulkEntries([{ username: "x", email }], salt);
      expect(entry.email_hash).toBe(want);
    }
  });

  it("preserves row order and count for a whole pasted roster", async () => {
    const rows = parseRosterInput("alice\nbob,bob@e.edu\ncarol");
    const entries = await toBulkEntries(rows, SALT);
    expect(entries.map((e) => e.username)).toEqual(["alice", "bob", "carol"]);
  });
});
