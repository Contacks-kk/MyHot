import type { ReactNode } from "react";
import { Link } from "react-router";
import { SITE } from "@aihot/site";
import { Wordmark } from "../../../brand/Logo.tsx";

export function AccountFrame({ title, description, children }: { title: string; description?: string; children: ReactNode }) {
  return (
    <main className="flex min-h-dvh items-center justify-center bg-bg px-4 py-10">
      <div className="w-full max-w-[440px]">
        <Link to="/" className="mx-auto mb-8 flex w-fit items-center gap-2 text-ink" aria-label={`${SITE.name} 首页`}>
          <Wordmark size={30} />
          <span className="text-[18px] font-semibold tracking-tight">{SITE.name}</span>
        </Link>
        <section className="card p-6 sm:p-8">
          <h1 className="text-[23px] font-semibold tracking-tight text-ink">{title}</h1>
          {description && <p className="mt-2 text-[13.5px] leading-relaxed text-ink-3">{description}</p>}
          {children}
        </section>
        <div className="mt-5 flex justify-center gap-4 text-[12px] text-ink-4">
          <Link to="/terms" className="hover:text-ink-2">使用规则</Link>
          <Link to="/privacy" className="hover:text-ink-2">隐私说明</Link>
          <Link to="/about" className="hover:text-ink-2">关于 MyHOT</Link>
        </div>
      </div>
    </main>
  );
}

export const fieldClass = "mt-1.5 h-11 w-full rounded-control border border-line-strong bg-surface px-3.5 text-[14px] text-ink outline-none transition-colors placeholder:text-ink-4 focus:border-accent";
export const labelClass = "mt-4 block text-[12.5px] font-medium text-ink-2";
export const submitClass = "mt-5 inline-flex h-11 w-full items-center justify-center rounded-full bg-accent px-5 text-[14px] font-semibold text-accent-contrast transition-colors hover:bg-accent-ink disabled:cursor-not-allowed disabled:opacity-55";

export async function postJSON<T>(path: string, payload: unknown, csrfToken?: string): Promise<T> {
  return sendJSON<T>(path, "POST", payload, csrfToken);
}

export async function putJSON<T>(path: string, payload: unknown, csrfToken?: string): Promise<T> {
  return sendJSON<T>(path, "PUT", payload, csrfToken);
}

export async function deleteJSON<T>(path: string, payload: unknown, csrfToken?: string): Promise<T> {
  return sendJSON<T>(path, "DELETE", payload, csrfToken);
}

async function sendJSON<T>(path: string, method: string, payload: unknown, csrfToken?: string): Promise<T> {
  const response = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", ...(csrfToken ? { "X-CSRF-Token": csrfToken } : {}) },
    body: JSON.stringify(payload),
  });
  if (response.status === 204) return undefined as T;
  const body = await response.json().catch(() => ({})) as { error?: string };
  if (!response.ok) throw new Error(body.error || `请求失败（${response.status}）`);
  return body as T;
}

export function FormMessage({ children, error = false }: { children: ReactNode; error?: boolean }) {
  return <p role={error ? "alert" : "status"} className={`mt-3 text-[12.5px] leading-relaxed ${error ? "text-hot" : "text-ink-3"}`}>{children}</p>;
}
