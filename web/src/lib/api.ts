import type {
  BatchSyncResult,
  Orphan,
  RbacRule,
  RuntimeConfig,
  SyncEvent,
  SyncResponse,
  Unit,
} from "./types";

// Always same-origin: the browser calls this Next.js server's own
// /api/proxy route, which forwards to the runcd API server-to-server
// (see app/api/proxy/[...path]/route.ts). The dashboard and the API are
// separate IAP-protected Cloud Run services, so the browser can't call the
// API's origin directly — IAP's session cookie is scoped to its own origin.
const API_BASE_URL = "/api/proxy";

export class ApiError extends Error {
  constructor(
    message: string,
    public readonly status: number,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

// errorMessage turns a failed response's body into something worth showing
// a user. The backend's own errors are plain text (e.g. "forbidden: no
// role grants sync access") and pass through unchanged — but an expired
// IAP session redirects to an HTML sign-in page before the request ever
// reaches the backend at all, and that HTML (or any other non-JSON,
// non-plain-text body) is not something to show verbatim.
function errorMessage(status: number, contentType: string | null, body: string): string {
  const trimmed = body.trim();
  const looksLikeHtml =
    (contentType?.includes("text/html") ?? false) || trimmed.startsWith("<");
  if (!trimmed || looksLikeHtml) {
    return `Session expired or an unexpected server response (HTTP ${status}) — try refreshing the page.`;
  }
  return trimmed;
}

// requestWithStatus is request<T>'s actual implementation, plus the raw
// response status alongside the parsed body. Almost every caller only
// needs the body (see request<T> below) — listOrphans is the one exception,
// since handleOrphans uses 206 vs. 200 to mean "this scan may be
// incomplete," a distinction the body alone can't carry.
async function requestWithStatus<T>(
  path: string,
  init?: RequestInit,
): Promise<{ body: T; status: number }> {
  const res = await fetch(`${API_BASE_URL}${path}`, {
    ...init,
    credentials: "include",
    headers: {
      Accept: "application/json",
      ...init?.headers,
    },
  });
  if (!res.ok) {
    const body = await res.text().catch(() => "");
    throw new ApiError(
      errorMessage(res.status, res.headers.get("content-type"), body),
      res.status,
    );
  }
  if (res.status === 204) {
    return { body: undefined as T, status: res.status };
  }
  try {
    return { body: (await res.json()) as T, status: res.status };
  } catch {
    // A 200 with a malformed/empty body (e.g. a proxy misconfiguration, or
    // a truncated response) would otherwise surface JSON.parse's raw
    // "Unexpected end of JSON input" to the user — the same class of
    // problem errorMessage() already handles for non-OK responses, so a
    // successful-status response deserves the same friendly treatment.
    throw new ApiError(
      `Unexpected server response (HTTP ${res.status}) — try refreshing the page.`,
      res.status,
    );
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const { body } = await requestWithStatus<T>(path, init);
  return body;
}

export function listUnits(): Promise<Unit[]> {
  return request<Unit[]>("/api/units");
}

export function getUnit(project: string, app: string): Promise<Unit> {
  return request<Unit>(
    `/api/units/${encodeURIComponent(project)}/${encodeURIComponent(app)}`,
  );
}

export function getUnitHistory(
  project: string,
  app: string,
): Promise<SyncEvent[]> {
  return request<SyncEvent[]>(
    `/api/units/${encodeURIComponent(project)}/${encodeURIComponent(app)}/history`,
  );
}

export function listRbac(): Promise<RbacRule[]> {
  return request<RbacRule[]>("/api/rbac");
}

export function getRuntimeConfig(): Promise<RuntimeConfig> {
  return request<RuntimeConfig>("/api/config");
}

export function syncUnit(
  project: string,
  app: string,
): Promise<SyncResponse> {
  return request<SyncResponse>(
    `/api/sync/${encodeURIComponent(project)}/${encodeURIComponent(app)}`,
    { method: "POST" },
  );
}

// listOrphans, unlike every other call here, needs the raw response status
// rather than just the parsed body: handleOrphans returns 206 (not 200)
// when some but not all project/region scans failed, so the result is
// only a partial view of the fleet — worth telling the user, not silently
// indistinguishable from a complete scan that found nothing. Built on
// requestWithStatus rather than a second hand-rolled fetch, so it still
// gets request<T>'s malformed-body/204 handling for free.
export async function listOrphans(): Promise<{
  orphans: Orphan[];
  partial: boolean;
}> {
  const { body, status } = await requestWithStatus<Orphan[]>("/api/orphans");
  return { orphans: body ?? [], partial: status === 206 };
}

export function syncBatch(opts: {
  project?: string;
  onlyOutOfSync?: boolean;
}): Promise<BatchSyncResult[]> {
  const params = new URLSearchParams();
  if (opts.project) params.set("project", opts.project);
  if (opts.onlyOutOfSync) params.set("filter", "outOfSync");
  const qs = params.toString();
  return request<BatchSyncResult[]>(`/api/sync${qs ? `?${qs}` : ""}`, {
    method: "POST",
  });
}
