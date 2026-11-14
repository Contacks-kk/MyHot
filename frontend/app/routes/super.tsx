import { useState } from "react";
import { useLoaderData } from "react-router";
import type { Route } from "./+types/super";
import { API_BASE_URL } from "../lib/api.server";
import { buttonClass } from "../components/ui/Controls";

type User = { id: number; email: string; verifiedAt: string | null; isAdmin: boolean };
type Principal = { csrf: string; super: boolean };

/** Load super administrator identity and the first page of users. */
export async function loader({ request }: Route.LoaderArgs) {
  const headers = { cookie: request.headers.get("cookie") ?? "", accept: "application/json" };
  const me = await fetch(`${API_BASE_URL}/api/admin/me`, { headers, signal: request.signal });
  if (me.status === 401) {
    return { principal: null, users: [] as User[], error: new URL(request.url).searchParams.get("error") };
  }
  if (!me.ok) {
    throw new Response("无法加载管理员会话", { status: 503 });
  }
  const principal = await me.json() as Principal;
  if (!principal.super) {
    return { principal: null, users: [] as User[], error: new URL(request.url).searchParams.get("error") };
  }
  const response = await fetch(`${API_BASE_URL}/api/super/users`, { headers, signal: request.signal });
  if (!response.ok) {
    throw new Response("无法加载用户列表", { status: 503 });
  }
  const result = await response.json() as { users: User[] };
  return { principal, users: result.users, error: null };
}

export const headers: Route.HeadersFunction = () => ({ "Cache-Control": "no-store", "X-Robots-Tag": "noindex, nofollow" });

/** Manage administrator access after authenticating with the super key. */
export default function SuperPage() {
  const data = useLoaderData<typeof loader>();
  const [users, setUsers] = useState(data.users);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [hasMore, setHasMore] = useState(data.users.length === 50);

  /** Change an account's administrator grant. */
  async function changeGrant(user: User) {
    if (!data.principal) {
      return;
    }
    setBusy(true);
    setError("");
    try {
      const response = await fetch(`/api/super/users/${user.id}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": data.principal.csrf },
        body: JSON.stringify({ isAdmin: !user.isAdmin }),
      });
      if (!response.ok) {
        throw new Error("更新失败，请重新登录后重试。");
      }
      setUsers((current) => current.map((item) => item.id === user.id ? { ...item, isAdmin: !item.isAdmin } : item));
    } catch (err) {
      setError(err instanceof Error ? err.message : "更新失败，请重试。");
    } finally {
      setBusy(false);
    }
  }

  /** Load the next page of users. */
  async function loadMore() {
    setBusy(true);
    setError("");
    try {
      const response = await fetch(`/api/super/users?after=${users[users.length - 1]?.id ?? 0}`);
      if (!response.ok) {
        throw new Error("加载失败，请重试。");
      }
      const result = await response.json() as { users: User[] };
      setUsers((current) => [...current, ...result.users]);
      setHasMore(result.users.length === 50);
    } catch (err) {
      setError(err instanceof Error ? err.message : "加载失败，请重试。");
    } finally {
      setBusy(false);
    }
  }

  if (!data.principal) {
    return <main className="flex min-h-dvh items-center justify-center bg-bg px-4">
      <div className="w-full max-w-[360px]">
        <h1 className="text-center text-2xl font-semibold text-ink">超级管理员</h1>
        <form method="post" action="/api/auth/password" className="card mt-8 p-6">
          <input type="hidden" name="return" value="/super" />
          <label htmlFor="super-key" className="block text-sm text-ink-2">管理员密钥</label>
          <input id="super-key" name="password" type="password" autoComplete="current-password" required autoFocus className="mt-2 h-10 w-full rounded-full border border-line-strong bg-surface px-4 text-ink" />
          {data.error && <p role="alert" className="mt-3 text-sm text-hot">{data.error === "too-many" ? "尝试次数过多，请稍后再试。" : data.error === "unset" ? "管理员密钥尚未配置。" : "密钥错误，请重试。"}</p>}
          <button className={`${buttonClass("primary", "lg")} mt-5 w-full`} type="submit">登录</button>
        </form>
      </div>
    </main>;
  }

  return <main className="min-h-dvh bg-bg px-5 py-12 text-ink">
    <div className="mx-auto max-w-4xl">
      <div className="flex flex-wrap items-center justify-between gap-4">
        <div><h1 className="text-3xl font-semibold">管理员权限</h1><p className="mt-2 text-sm text-ink-3">为用户授权后，他们可以使用自己的邮箱和密码登录后台。</p></div>
        <a className={buttonClass("secondary", "lg")} href="/admin">进入后台</a>
      </div>
      {error && <p role="alert" className="mt-5 text-sm text-hot">{error}</p>}
      <div className="card mt-8 divide-y divide-line">
        {users.length === 0 && <p className="p-6 text-ink-3">暂无用户</p>}
        {users.map((user) => <div key={user.id} className="flex flex-wrap items-center justify-between gap-4 p-5">
          <div><p className="font-medium">{user.email}</p><p className="mt-1 text-sm text-ink-3">{user.verifiedAt ? "邮箱已验证" : "邮箱未验证"} · {user.isAdmin ? "管理员" : "普通用户"}</p></div>
          <button type="button" disabled={busy} onClick={() => void changeGrant(user)} className={buttonClass("secondary", "md")}>{user.isAdmin ? "撤销管理员" : "设为管理员"}</button>
        </div>)}
      </div>
      {hasMore && <button type="button" disabled={busy} onClick={() => void loadMore()} className={`${buttonClass("secondary", "md")} mt-5`}>加载更多</button>}
    </div>
  </main>;
}
