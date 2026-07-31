import type {
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

async function request<T>(path: string, init?: RequestInit): Promise<T> {
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
    return undefined as T;
  }
  return (await res.json()) as T;
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
