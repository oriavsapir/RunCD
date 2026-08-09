import { afterEach, describe, expect, it, vi } from "vitest";
import {
  ApiError,
  getUnit,
  getUnitHistory,
  listOrphans,
  listUnits,
  syncBatch,
} from "./api";

function mockFetchOnce(status: number, body: string, contentType?: string) {
  const fn = vi.fn().mockResolvedValue(
    new Response(body, {
      status,
      headers: contentType ? { "content-type": contentType } : undefined,
    }),
  );
  vi.stubGlobal("fetch", fn);
  return fn;
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

  it("replaces a malformed body on an otherwise-successful response with a clear message instead of a raw JSON.parse error", async () => {
    mockFetchOnce(200, "{not valid json", "application/json");
    await expect(listUnits()).rejects.toSatisfy((err: unknown) => {
      expect(err).toBeInstanceOf(ApiError);
      expect((err as ApiError).message).toMatch(/unexpected server response/i);
      expect((err as ApiError).message).not.toMatch(/json/i);
      return true;
    });
  });
});

describe("request paths and query strings", () => {
  it("percent-encodes project/app in getUnit's URL", async () => {
    const fn = mockFetchOnce(200, "{}", "application/json");
    await getUnit("proj/with space", "app@v1");
    expect(fn).toHaveBeenCalledWith(
      "/api/proxy/api/units/proj%2Fwith%20space/app%40v1",
      expect.anything(),
    );
  });

  it("percent-encodes project/app in getUnitHistory's URL", async () => {
    const fn = mockFetchOnce(200, "[]", "application/json");
    await getUnitHistory("proj/with space", "app@v1");
    expect(fn).toHaveBeenCalledWith(
      "/api/proxy/api/units/proj%2Fwith%20space/app%40v1/history",
      expect.anything(),
    );
  });

  it("builds no query string for syncBatch with no options", async () => {
    const fn = mockFetchOnce(200, "[]", "application/json");
    await syncBatch({});
    expect(fn).toHaveBeenCalledWith("/api/proxy/api/sync", expect.anything());
  });

  it("includes only project in syncBatch's query string when onlyOutOfSync is unset", async () => {
    const fn = mockFetchOnce(200, "[]", "application/json");
    await syncBatch({ project: "acme" });
    expect(fn).toHaveBeenCalledWith(
      "/api/proxy/api/sync?project=acme",
      expect.anything(),
    );
  });

  it("includes only filter in syncBatch's query string when project is unset", async () => {
    const fn = mockFetchOnce(200, "[]", "application/json");
    await syncBatch({ onlyOutOfSync: true });
    expect(fn).toHaveBeenCalledWith(
      "/api/proxy/api/sync?filter=outOfSync",
      expect.anything(),
    );
  });

  it("includes both project and filter in syncBatch's query string when both are set", async () => {
    const fn = mockFetchOnce(200, "[]", "application/json");
    await syncBatch({ project: "acme", onlyOutOfSync: true });
    expect(fn).toHaveBeenCalledWith(
      "/api/proxy/api/sync?project=acme&filter=outOfSync",
      expect.anything(),
    );
  });

  it("returns undefined for a 204 response instead of trying to parse a body", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(null, { status: 204 })),
    );
    await expect(listUnits()).resolves.toBeUndefined();
  });
});

describe("listOrphans", () => {
  it("reports partial: false on a plain 200", async () => {
    mockFetchOnce(
      200,
      JSON.stringify([{ project: "acme", region: "us-central1", app: "old" }]),
      "application/json",
    );
    await expect(listOrphans()).resolves.toEqual({
      orphans: [{ project: "acme", region: "us-central1", app: "old" }],
      partial: false,
    });
  });

  it("reports partial: true on a 206 (some project/region scans failed)", async () => {
    mockFetchOnce(206, "[]", "application/json");
    await expect(listOrphans()).resolves.toEqual({
      orphans: [],
      partial: true,
    });
  });

  it("surfaces a forbidden response as an ApiError, same as every other endpoint", async () => {
    mockFetchOnce(403, "forbidden: no role grants sync access", "text/plain");
    await expect(listOrphans()).rejects.toSatisfy((err: unknown) => {
      expect(err).toBeInstanceOf(ApiError);
      expect((err as ApiError).message).toBe(
        "forbidden: no role grants sync access",
      );
      return true;
    });
  });

  it("replaces a malformed body on a successful status with a clear message", async () => {
    mockFetchOnce(200, "{not valid json", "application/json");
    await expect(listOrphans()).rejects.toSatisfy((err: unknown) => {
      expect(err).toBeInstanceOf(ApiError);
      expect((err as ApiError).message).toMatch(/unexpected server response/i);
      return true;
    });
  });

  it("treats a 204 (no body) as an empty, non-partial orphan list instead of throwing", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(null, { status: 204 })),
    );
    await expect(listOrphans()).resolves.toEqual({
      orphans: [],
      partial: false,
    });
  });
});
