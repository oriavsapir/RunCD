import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { NextRequest } from "next/server";

const getRequestHeaders = vi.fn().mockResolvedValue(new Map());
const getIdTokenClient = vi.fn().mockResolvedValue({ getRequestHeaders });

vi.mock("google-auth-library", () => ({
  // A plain function, not an arrow function — arrow functions can't be
  // called with `new`, which is exactly how route.ts constructs this.
  GoogleAuth: vi.fn().mockImplementation(function () {
    return { getIdTokenClient };
  }),
}));

const ORIGINAL_API_URL = process.env.RUNCD_API_URL;

function makeRequest(path: string, init?: RequestInit) {
  return new NextRequest(`http://localhost/api/proxy${path}`, init);
}

describe("proxy route", () => {
  beforeEach(() => {
    vi.resetModules();
    process.env.RUNCD_API_URL = "https://runcd-api-internal.example.run.app";
    getRequestHeaders.mockClear();
    getIdTokenClient.mockClear();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    if (ORIGINAL_API_URL === undefined) {
      delete process.env.RUNCD_API_URL;
    } else {
      process.env.RUNCD_API_URL = ORIGINAL_API_URL;
    }
  });

  it("forwards ?project=/?filter= query params unchanged to the backend", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response("[]", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const { POST } = await import("./route");
    await POST(
      makeRequest("/api/sync?project=acme&filter=outOfSync", { method: "POST" }),
      { params: Promise.resolve({ path: ["api", "sync"] }) },
    );

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [calledUrl, calledInit] = fetchMock.mock.calls[0];
    expect(calledUrl).toBe(
      "https://runcd-api-internal.example.run.app/api/sync?project=acme&filter=outOfSync",
    );
    expect(calledInit.redirect).toBe("error");
  });

  it("rejects redirects rather than following them, so auth headers can't leak to a redirect target", async () => {
    const fetchMock = vi
      .fn()
      .mockRejectedValue(new TypeError("unexpected redirect"));
    vi.stubGlobal("fetch", fetchMock);

    const { GET } = await import("./route");
    await expect(
      GET(makeRequest("/api/units"), {
        params: Promise.resolve({ path: ["api", "units"] }),
      }),
    ).rejects.toThrow();

    expect(fetchMock.mock.calls[0][1].redirect).toBe("error");
  });

  it("returns a 500 with no upstream call when RUNCD_API_URL isn't configured", async () => {
    delete process.env.RUNCD_API_URL;
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    const { GET } = await import("./route");
    const res = await GET(makeRequest("/api/units"), {
      params: Promise.resolve({ path: ["api", "units"] }),
    });

    expect(res.status).toBe(500);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("forwards the IAP assertion under the renamed header, not the original name", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const { GET } = await import("./route");
    await GET(
      makeRequest("/api/units", {
        headers: { "x-goog-iap-jwt-assertion": "signed-assertion" },
      }),
      { params: Promise.resolve({ path: ["api", "units"] }) },
    );

    const calledInit = fetchMock.mock.calls[0][1];
    expect(calledInit.headers["x-runcd-iap-assertion"]).toBe("signed-assertion");
    expect(calledInit.headers["x-goog-iap-jwt-assertion"]).toBeUndefined();
  });
});
