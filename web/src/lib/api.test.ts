import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, listUnits } from "./api";

function mockFetchOnce(status: number, body: string, contentType?: string) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(body, {
        status,
        headers: contentType ? { "content-type": contentType } : undefined,
      }),
    ),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("request error handling", () => {
  it("passes the backend's own plain-text error through unchanged", async () => {
    mockFetchOnce(403, "forbidden: no role grants sync access", "text/plain");
    await expect(listUnits()).rejects.toSatisfy((err: unknown) => {
      expect(err).toBeInstanceOf(ApiError);
      expect((err as ApiError).message).toBe(
        "forbidden: no role grants sync access",
      );
      return true;
    });
  });

  it("replaces an HTML error body (e.g. an IAP sign-in redirect) with a clear message", async () => {
    mockFetchOnce(401, "<html><body>Sign in</body></html>", "text/html");
    await expect(listUnits()).rejects.toSatisfy((err: unknown) => {
      expect(err).toBeInstanceOf(ApiError);
      expect((err as ApiError).message).toMatch(/session expired|unexpected server response/i);
      expect((err as ApiError).message).not.toContain("<html>");
      return true;
    });
  });

  it("replaces an empty error body with a clear message", async () => {
    mockFetchOnce(500, "");
    await expect(listUnits()).rejects.toSatisfy((err: unknown) => {
      expect((err as ApiError).message).toMatch(/unexpected server response/i);
      return true;
    });
  });
});
