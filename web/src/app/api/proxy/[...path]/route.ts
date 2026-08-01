import { GoogleAuth } from "google-auth-library";
import { type NextRequest, NextResponse } from "next/server";

// Server-side proxy to the runcd API. The dashboard and the API are two
// separate Cloud Run services with no shared domain, so a browser can't
// call the API directly — IAP's session cookie is scoped to its own
// origin and wouldn't be sent on a cross-site fetch. Instead this route
// runs on the dashboard's own Cloud Run service account and calls the API
// server-to-server.
//
// Two independent controls, since IAP's own audience validation only
// supports the email-less machine-to-machine case (no OAuth brand exists
// for direct-Cloud-Run IAP, and the legacy OAuth Admin API IAP would
// otherwise need is being shut down) — stacking two IAP layers can't work,
// so only the dashboard has IAP:
//   - Cloud Run IAM invoker (audience = the API's own URL) gates *who can
//     call* — only this service's account, via the standard
//     service-to-service pattern: https://docs.cloud.google.com/run/docs/authenticating/service-to-service
//   - the human's IAP assertion, forwarded verbatim, tells the API *which
//     human* — the API's IAPAuthenticator verifies it against the
//     dashboard's own audience and extracts the email RBAC checks against.
const API_BASE_URL = process.env.RUNCD_API_URL;

const auth = new GoogleAuth();

// Cached at module scope, not re-created per request: getIdTokenClient
// returns a fresh IdTokenClient wrapper with no token of its own each call
// — without reusing the same instance across requests, every proxied
// dashboard call (units, diff, history, sync) re-mints a token instead of
// reusing the one the client already caches internally until near expiry.
// Caching the promise itself, not just the resolved client, so concurrent
// requests during a cold start all await the same in-flight mint rather
// than each kicking off their own.
let idTokenClientPromise: ReturnType<GoogleAuth["getIdTokenClient"]> | null = null;

function getIdTokenClient() {
  if (!idTokenClientPromise) {
    idTokenClientPromise = auth.getIdTokenClient(API_BASE_URL!);
  }
  return idTokenClientPromise;
}

async function forward(req: NextRequest, path: string[]) {
  if (!API_BASE_URL) {
    return NextResponse.json(
      { error: "RUNCD_API_URL is not configured" },
      { status: 500 },
    );
  }

  const client = await getIdTokenClient();
  const authHeaders = await client.getRequestHeaders();
  const iapAssertion = req.headers.get("x-goog-iap-jwt-assertion");
  const url = `${API_BASE_URL}/${path.join("/")}${req.nextUrl.search}`;

  const res = await fetch(url, {
    method: req.method,
    headers: {
      ...Object.fromEntries(authHeaders),
      Accept: "application/json",
      // Google's frontend strips any client-supplied X-Goog-* header before
      // it reaches Cloud Run (anti-spoofing for what's normally
      // infra-injected-only) — the assertion has to travel under a
      // different name than the one IAP itself used to attach it to this
      // request. internal/auth's IAPAuthenticator reads this same name.
      ...(iapAssertion ? { "x-runcd-iap-assertion": iapAssertion } : {}),
    },
    // Only sync (POST) has ever sent a body, and it's always been empty,
    // which silently masked this never being forwarded at all — any future
    // POST/PUT/PATCH endpoint with a real request body would otherwise
    // reach the API empty.
    body: req.body,
    // Required by Node's fetch whenever body is a stream.
    duplex: req.body ? "half" : undefined,
  } as RequestInit);
  const body = await res.text();
  return new NextResponse(body, {
    status: res.status,
    headers: {
      "content-type": res.headers.get("content-type") ?? "application/json",
    },
  });
}

type RouteParams = { params: Promise<{ path: string[] }> };

export async function GET(req: NextRequest, { params }: RouteParams) {
  const { path } = await params;
  return forward(req, path);
}

export async function POST(req: NextRequest, { params }: RouteParams) {
  const { path } = await params;
  return forward(req, path);
}
