import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach, vi } from "vitest";

// Explicit, not relying on auto-cleanup detection: a dialog left mounted
// (or a mock not restored) from one test can otherwise leak into the next
// test's queries — the exact failure mode a base-ui AlertDialog's
// animate-out state hit for sync-button.test.tsx.
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});
