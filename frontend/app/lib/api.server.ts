// SSR loaders read Go API content through HTTP with the current user Cookie.
// The web process never touches the database; account responses are never cached.
import { data, redirect } from "react-router";
import { logError } from "./errors.server.ts";

/** The single business backend used by SSR loaders. Browser requests pass through the same boundary. */
export const API_BASE_URL = process.env.GO_API_BASE_URL || "http://127.0.0.1:3002";

/** An api answer other than 2xx. A merged story answers 308 with the story it now lives in. */
class ApiError extends Error {
  readonly status: number;
  readonly mergedInto: string | null;
  readonly requestId: string | null;
  constructor(status: number, mergedInto: string | null, requestId: string | null) {
    super(`api ${status}`);
    this.status = status;
    this.mergedInto = mergedInto;
    this.requestId = requestId;
  }
}

export async function apiGet<T>(path: string, init?: { request?: Request; signal?: AbortSignal; headers?: Record<string, string> }): Promise<T> {
  try {
    const res = await fetch(`${API_BASE_URL}${path}`, {
      headers: { accept: "application/json", "x-aihot-ssr": "1", ...(init?.request ? { cookie: init.request.headers.get("cookie") ?? "" } : {}), ...init?.headers },
      redirect: "manual",
      cache: "no-store",
      signal: init?.signal ? AbortSignal.any([init.signal, AbortSignal.timeout(15_000)]) : AbortSignal.timeout(15_000),
    });
    if (!res.ok) {
      const body = (await res.json().catch(() => null)) as { mergedInto?: string } | null;
      throw new ApiError(res.status, res.status === 308 ? (body?.mergedInto ?? null) : null, res.headers.get("X-Request-Id"));
    }
    return (await res.json()) as T;
  } catch (error) {
    if (!init?.signal?.aborted && (!(error instanceof ApiError) || error.status >= 500)) {
      logError(error, { msg: "ssr api request failed", method: "GET", path,
        ...(error instanceof ApiError ? { status: error.status, upstreamRequestId: error.requestId } : {}),
      });
    }
    throw error;
  }
}

/**
 * Maps API failures to route responses: real 404s, the permanent redirect of a merged page (`merged`
 * gives the page of the id it was merged into), the search-busy page, otherwise 503.
 */
export async function loadOr404<T>(path: string, opts: { request?: Request; busyRedirect?: string; merged?: (id: string) => string; signal?: AbortSignal } = {}): Promise<T> {
  try {
    return await apiGet<T>(path, { request: opts.request, signal: opts.signal });
  } catch (error) {
    if (opts.signal?.aborted) throw error;
    if (error instanceof ApiError) {
      if (error.status === 401) {
        const target = opts.request ? new URL(opts.request.url) : null;
        throw redirect(`/login?${new URLSearchParams({ return: target ? target.pathname + target.search : "/" })}`);
      }
      if (error.status === 404) throw data({ message: "not_found" }, { status: 404 });
      if (error.mergedInto && opts.merged) throw redirect(opts.merged(error.mergedInto), 308);
      if (error.status === 503 && opts.busyRedirect) throw redirect(opts.busyRedirect);
      if (error.status === 400) throw data({ message: "bad_request" }, { status: 400 });
    }
    throw data({ message: "unavailable" }, { status: 503 });
  }
}

/** SSR documents include account identity and must never enter a shared cache. */
export function privatePageHeaders(): Record<string, string> {
  return { "Cache-Control": "private, no-store", "X-Accel-Expires": "0" };
}
