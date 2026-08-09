import { defineConfig } from "vitest/config";

// Deliberately standalone rather than merged into vite.config.ts: the test
// targets are pure functions (roster parsing/hashing, URL builders), so they
// need neither the React plugin nor the dev proxy. Keeping this separate also
// means test configuration cannot affect the production build.
//
// The environment is "node", not jsdom: nothing here renders a component. The
// one browser API a test needs — window.location.origin — is stubbed per-test,
// which is both lighter than a DOM implementation and lets a test assert the
// URL for several origins instead of whichever one jsdom defaults to.
// Web Crypto (crypto.subtle), which the hashing path uses, is built into Node.
export default defineConfig({
  test: {
    environment: "node",
    include: ["src/**/*.test.ts"],
  },
});
