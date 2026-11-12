import type { Screen } from "../components/shell/screens";
import { useState, type FormEvent } from "react";
import { Link, useLoaderData, useRevalidator } from "react-router";
import type { Route } from "./+types/model-settings";
import { fieldClass, FormMessage, labelClass, deleteJSON, postJSON, putJSON } from "../features/account/common";
import { userApiGet, userApiMe } from "../lib/user-api.server";

export const handle: Screen = { tab: "me", name: "模型配置" };

export async function loader({ request }: Route.LoaderArgs) {
  const [user, model] = await Promise.all([
    userApiMe(request),
    userApiGet<{ configured: boolean; baseUrl: string; model: string; apiKeySet?: boolean }>(request, "/api/me/model"),
  ]);
  return { csrfToken: user?.csrfToken ?? "", ...model };
}

export const meta: Route.MetaFunction = () => [{ title: "模型配置 · MyHOT" }, { name: "robots", content: "noindex, nofollow" }];

export const headers: Route.HeadersFunction = () => ({ "Cache-Control": "private, no-store", "X-Robots-Tag": "noindex, nofollow" });

export default function ModelSettings() {
  const initial = useLoaderData<typeof loader>();
  const revalidator = useRevalidator();
  const [baseUrl,setBaseUrl]=useState(initial.baseUrl); const [model,setModel]=useState(initial.model); const [apiKey,setApiKey]=useState("");
  const [pendingTest,setPendingTest]=useState<number|null>(null);
  const [busy,setBusy]=useState(false);const [message,setMessage]=useState("");const [error,setError]=useState("");
  async function save(event:FormEvent<HTMLFormElement>){event.preventDefault();setBusy(true);setError("");setMessage("");try{await putJSON("/api/me/model",{baseUrl,model,apiKey},initial.csrfToken);setApiKey("");setPendingTest(null);void revalidator.revalidate();setMessage("模型配置已保存，只会用于之后命中的新文章。");}catch(e){setError(e instanceof Error?e.message:"保存失败");}finally{setBusy(false);}}
  async function test(){
    setBusy(true); setError(""); setMessage("");
    try {
      const testId = pendingTest ?? (await postJSON<{testId:number}>("/api/me/model/test", {baseUrl,model,apiKey}, initial.csrfToken)).testId;
      setPendingTest(testId);
      setMessage("连接测试已排队，正在等待结果…");
      const deadline = Date.now() + 70_000;
      while (Date.now() < deadline) {
        await new Promise(resolve => setTimeout(resolve, 2000));
        const remaining = deadline - Date.now();
        if (remaining <= 0) break;
        const response = await fetch(`/api/me/model/test/${testId}`, { credentials: "same-origin", cache: "no-store", signal: AbortSignal.timeout(Math.min(5000, remaining)) });
        const result = await response.json() as {status:string;usage?:{prompt_tokens:number;completion_tokens:number};error?:string};
        if (!response.ok) throw new Error(result.error || "无法读取测试结果");
        if (result.status === "failed") { setPendingTest(null); throw new Error(result.error || "连接测试失败"); }
        if (result.status === "completed") { setPendingTest(null); setMessage(`连接成功 · 输入 ${result.usage?.prompt_tokens ?? 0} tokens，输出 ${result.usage?.completion_tokens ?? 0} tokens`); return; }
      }
      setMessage("测试仍在后台处理中，稍后点击查看测试结果。");
    } catch(e) { setMessage(""); setError(e instanceof Error ? e.message : "连接测试失败"); }
    finally { setBusy(false); }
  }
  return <main className="mx-auto max-w-[720px] pb-10"><Link to="/" className="text-[12px] text-ink-4 hover:text-accent">← 我的热点</Link><h1 className="mt-3 text-[25px] font-semibold tracking-tight text-ink">模型配置</h1><p className="mt-2 text-[13px] leading-relaxed text-ink-3">填写任意兼容 OpenAI Chat Completions 的公网 HTTPS 接口。MyHOT 会使用此配置生成你的摘要和日报。</p>
    <form onSubmit={save} className="card mt-6 p-5 sm:p-6">
      <label className={labelClass} htmlFor="baseUrl">Base URL</label><input className={fieldClass} id="baseUrl" name="baseUrl" type="url" autoComplete="url" placeholder="https://api.example.com/v1" value={baseUrl} onChange={e=>setBaseUrl(e.target.value)} required/>
      <label className={labelClass} htmlFor="model">模型名称</label><input className={fieldClass} id="model" name="model" value={model} onChange={e=>setModel(e.target.value)} placeholder="gpt-4o-mini" maxLength={160} required/>
      <label className={labelClass} htmlFor="apiKey">API Key {initial.apiKeySet&&<span className="font-normal text-ink-4">（已保存；留空保持不变）</span>}</label><input className={fieldClass} id="apiKey" name="apiKey" type="password" autoComplete="new-password" value={apiKey} onChange={e=>setApiKey(e.target.value)} placeholder={initial.apiKeySet?"••••••••••••••••":"粘贴 API Key"} required={!initial.apiKeySet}/>
      <p className="mt-3 text-[11.5px] leading-relaxed text-ink-4">API Key 加密保存。文章内容会发送到你配置的模型服务商，由该服务商按你的 API Key 计费；测试连接会发送一次请求。</p>
      {error&&<FormMessage error>{error}</FormMessage>}{message&&<FormMessage>{message}</FormMessage>}
      <div className="mt-5 flex flex-wrap gap-2"><button className="inline-flex h-10 items-center rounded-full bg-accent px-5 text-[13px] font-semibold text-accent-contrast hover:bg-accent-ink disabled:opacity-50" disabled={busy}>{busy?"请稍候…":"保存模型"}</button><button type="button" onClick={test} className="inline-flex h-10 items-center rounded-full border border-line-strong px-5 text-[13px] font-medium text-ink-2 hover:bg-bg-sunk disabled:opacity-50" disabled={busy||(!apiKey&&!initial.apiKeySet)}>{busy?"请稍候…":pendingTest?"查看测试结果":"测试连接"}</button></div>
    </form>
    <section className="mt-8 rounded-card border border-line bg-surface p-5"><h2 className="text-[15px] font-semibold text-ink">删除账号</h2><p className="mt-1.5 text-[12px] leading-relaxed text-ink-4">删除后会清除你的模型设置、关注项、会话、个人摘要和日报；共享信源与文章继续保留。</p><DeleteAccount csrfToken={initial.csrfToken}/></section>
  </main>;
}

function DeleteAccount({csrfToken}:{csrfToken:string}){
  const [password,setPassword]=useState("");const [confirm,setConfirm]=useState(false);const [busy,setBusy]=useState(false);const [error,setError]=useState("");
  async function remove(event:FormEvent<HTMLFormElement>){event.preventDefault();if(!confirm)return;setBusy(true);setError("");try{await deleteJSON("/api/me",{password},csrfToken);window.location.assign("/");}catch(e){setError(e instanceof Error?e.message:"删除失败");}finally{setBusy(false);}}
  return <form onSubmit={remove} className="mt-3"><label className={labelClass} htmlFor="confirmPassword">输入当前密码确认</label><input className={fieldClass} id="confirmPassword" type="password" value={password} onChange={e=>setPassword(e.target.value)} required autoComplete="current-password"/><label className="mt-3 flex items-start gap-2 text-[12px] text-ink-3"><input type="checkbox" className="mt-0.5 accent-hot" checked={confirm} onChange={e=>setConfirm(e.target.checked)}/>我理解此操作会立即删除个人数据</label>{error&&<FormMessage error>{error}</FormMessage>}<button disabled={busy||!confirm} className="mt-4 h-9 rounded-full border border-hot/40 px-4 text-[12px] font-medium text-hot hover:bg-hot/5 disabled:opacity-45">{busy?"正在删除…":"永久删除账号"}</button></form>;
}
