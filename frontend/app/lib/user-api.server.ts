import { data, redirect } from "react-router";

export const USER_API_BASE_URL = process.env.GO_API_BASE_URL || "http://127.0.0.1:3002";

export interface UserSession {
  id: number;
  email: string;
  csrfToken: string;
}

export class UserApiError extends Error {
  readonly status: number;
  constructor(status: number, message = "用户服务暂时不可用") { super(message); this.status = status; }
}

async function fetchUserApi<T>(request: Request, path: string): Promise<T | null> {
  let response: Response;
  try {
    response = await fetch(`${USER_API_BASE_URL}${path}`, {
      headers: { accept: "application/json", cookie: request.headers.get("cookie") ?? "" },
      signal: request.signal.aborted ? request.signal : AbortSignal.any([request.signal, AbortSignal.timeout(10_000)]),
      cache: "no-store",
    });
  } catch (error) {
    if (request.signal.aborted) throw error;
    throw new UserApiError(503);
  }
  if (response.status === 401) return null;
  if (!response.ok) throw new UserApiError(response.status);
  return await response.json() as T;
}

export async function userApiMe(request: Request): Promise<UserSession | null> {
  return requestJSON<UserSession>(request, "/api/me");
}

export async function userApiGet<T>(request: Request, path: string, options?: { notFound?: boolean }): Promise<T> {
  const value = await requestJSON<T>(request, path, options?.notFound);
  if (value === null) throw redirect(`/login?${new URLSearchParams({ return: new URL(request.url).pathname + new URL(request.url).search })}`, { headers: { "Cache-Control": "private, no-store" } });
  return value;
}

async function requestJSON<T>(request: Request, path: string, notFound = false): Promise<T | null> {
  try {
    return await fetchUserApi<T>(request, path);
  } catch (error) {
    if (error instanceof UserApiError && error.status === 404 && notFound) throw data({ message: "not_found" }, { status: 404 });
    if (error instanceof UserApiError) throw data({ message: "account_service_unavailable" }, { status: 503 });
    throw error;
  }
}

