import type { SyncEvent, SyncResponse, Unit } from "./types";

// Empty means same-origin: the dashboard is expected to sit behind the
// same Identity-Aware Proxy perimeter as the argorun API (either the same
// Cloud Run service, or two services fronted by one IAP-protected load
// balancer with path-based routing) — the browser's existing IAP session
// cookie then authenticates API calls automatically. Set
// NEXT_PUBLIC_API_BASE_URL only if the API is truly on a different origin.
const API_BASE_URL = process.env.NEXT_PUBLIC_API_BASE_URL ?? "";

export class ApiError extends Error {
  constructor(
    message: string,
    public readonly status: number,
  ) {
    super(message);
    this.name = "ApiError";
  }
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
    throw new ApiError(body || res.statusText, res.status);
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

export function syncUnit(
  project: string,
  app: string,
): Promise<SyncResponse> {
  return request<SyncResponse>(
    `/api/sync/${encodeURIComponent(project)}/${encodeURIComponent(app)}`,
    { method: "POST" },
  );
}
