// Parsing and hashing for bulk roster entry.
//
// The privacy rule this file exists to enforce: a plaintext email address must
// never reach the API. RosterEntry.EmailHash is documented as "a salted, one-way
// hash used only for client-side re-matching against an LMS pull" — so when an
// instructor pastes an LMS export with an email column, the address is hashed
// here, in the browser, and only the digest is sent.

import type { BulkRosterEntry } from "./api";

/** A parsed input line, before hashing. */
export interface ParsedRow {
  username: string;
  /** A plaintext address that still needs hashing. */
  email?: string;
  /** An already-hashed value, passed through untouched. */
  emailHash?: string;
}

/**
 * parseRosterInput reads either one username per line, or two-column CSV.
 *
 * Accepted per line:
 *   octocat                          → username only
 *   octocat,student@example.edu      → username + plaintext email (hashed later)
 *   octocat,<64-hex>                 → username + an already-computed hash
 *
 * Blank lines are skipped. A leading header row (`username,...`) is ignored, so
 * a CSV exported straight from an LMS works without hand-editing. Surrounding
 * quotes and whitespace are stripped. Lines beyond the second column are
 * ignored rather than rejected — LMS exports carry extra columns, and failing
 * the whole paste over them would be hostile.
 */
export function parseRosterInput(text: string): ParsedRow[] {
  const rows: ParsedRow[] = [];
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line) continue;

    const cells = line.split(",").map((c) => c.trim().replace(/^"(.*)"$/, "$1"));
    const username = cells[0];
    if (!username) continue;

    // Skip a header row, but only when it is genuinely a header: a bare
    // "username" line with no second column is a plausible (if odd) username.
    if (cells.length > 1 && /^(username|user|login|github)$/i.test(username)) continue;

    const second = cells[1] ?? "";
    if (!second) {
      rows.push({ username });
    } else if (isHexDigest(second)) {
      rows.push({ username, emailHash: second });
    } else {
      rows.push({ username, email: second });
    }
  }
  return rows;
}

/** A 64-char hex string is treated as an already-computed SHA-256 digest. */
function isHexDigest(s: string): boolean {
  return /^[0-9a-f]{64}$/i.test(s);
}

/**
 * toBulkEntries hashes any plaintext addresses and returns the API payload.
 *
 * Hashing is salted with the classroom ID so the same address in two different
 * classrooms does not produce the same digest — that keeps a digest from acting
 * as a cross-classroom identifier for a student.
 *
 * SubtleCrypto is only available in a secure context. Where it is missing
 * (plain http on a non-localhost host) the address is dropped rather than sent
 * in the clear: losing an optional matching hint is strictly better than
 * leaking the address the model forbids storing.
 */
export async function toBulkEntries(rows: ParsedRow[], salt: string): Promise<BulkRosterEntry[]> {
  const out: BulkRosterEntry[] = [];
  for (const row of rows) {
    if (row.emailHash) {
      out.push({ username: row.username, email_hash: row.emailHash });
      continue;
    }
    if (row.email && globalThis.crypto?.subtle) {
      out.push({ username: row.username, email_hash: await sha256Hex(`${salt}:${row.email.toLowerCase()}`) });
      continue;
    }
    out.push({ username: row.username });
  }
  return out;
}

async function sha256Hex(input: string): Promise<string> {
  const bytes = new TextEncoder().encode(input);
  const digest = await globalThis.crypto.subtle.digest("SHA-256", bytes);
  return Array.from(new Uint8Array(digest))
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}
