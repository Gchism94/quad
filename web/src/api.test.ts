import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";

// Backfilled from CC-CA3, which asked for this test but could not write one:
// the project had no test runner, so the invite URL was verified by inspecting
// the built bundle and comparing the shape against the server's route by hand.

// inviteUrl reads window.location.origin. The test environment is "node", so
// window is stubbed per-case — which also lets each case assert a different
// origin instead of whichever one a DOM implementation would default to.
function withOrigin(origin: string) {
  vi.stubGlobal("window", { location: { origin } });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("api.inviteUrl", () => {
  it("builds the student join URL for an assignment", () => {
    withOrigin("https://cairn.example.edu");
    expect(api.inviteUrl("a1b2c3")).toBe(
      "https://cairn.example.edu/assignments/a1b2c3/accept",
    );
  });

  it("matches the route the Go server registers", () => {
    // internal/api/server.go registers GET /assignments/{id}/accept. If that
    // path ever changes, this assertion is what should fail.
    withOrigin("https://cairn.example.edu");
    const url = new URL(api.inviteUrl("xyz"));
    expect(url.pathname).toBe("/assignments/xyz/accept");
  });

  it("is absolute, because the link is pasted into an LMS or an email", () => {
    // A same-origin relative path would be useless to the student receiving it.
    withOrigin("https://cairn.example.edu");
    const url = api.inviteUrl("a1");
    expect(url.startsWith("https://")).toBe(true);
    expect(() => new URL(url)).not.toThrow();
  });

  it("follows the deployment's own origin, including a port", () => {
    withOrigin("http://localhost:8080");
    expect(api.inviteUrl("a1")).toBe("http://localhost:8080/assignments/a1/accept");
  });

  it("does not collide with the grades CSV URL builder", () => {
    // Both build URLs from the same BASE; a copy-paste error between them would
    // hand students a download link.
    withOrigin("https://cairn.example.edu");
    expect(api.inviteUrl("a1")).not.toBe(api.gradesCsvUrl("a1"));
    expect(api.gradesCsvUrl("c1")).toBe("/classrooms/c1/grades.csv");
  });
});
