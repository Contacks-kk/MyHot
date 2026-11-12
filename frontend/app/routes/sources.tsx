import { useState, type FormEvent } from "react";
import { Link, useLoaderData } from "react-router";
import type { Route } from "./+types/sources";
import type { Screen } from "../components/shell/screens";
import { FormMessage, fieldClass } from "../features/account/common";
import { userApiGet, userApiMe } from "../lib/user-api.server";

export const handle: Screen = { tab: "me", name: "我的信源" };

type Subscription = {
  id: number; sourceId: string; name: string; url: string; enabled: boolean;
  intervalMinutes: number; reuseWindowMinutes: number;
  effectiveIntervalMinutes: number; effectiveReuseWindowMinutes: number;
  lastCheckedAt: string | null; nextFetchAt: string | null; health: string;
};
type FetchResult = {
  jobId: number; status: "queued" | "running" | "succeeded" | "reused" | "failed" | string;
  checkedAt: string | null; foundCount: number; newCount: number; error?: string;
  items: Array<{title:string;url:string;publishedAt:string|null}>;
};

export async function loader({ request }: Route.LoaderArgs) {
  const [user, result] = await Promise.all([
    userApiMe(request),
    userApiGet<{sources:Subscription[]}>(request,"/api/me/sources"),
  ]);
  return { csrfToken:user?.csrfToken??"", sources:result.sources };
}

export const meta: Route.MetaFunction = () => [{ title:"我的信源 · MyHOT" },{name:"robots",content:"noindex, nofollow"}];
export const headers: Route.HeadersFunction = () => ({"Cache-Control":"private, no-store","X-Robots-Tag":"noindex, nofollow"});

const emptyForm = {name:"",url:"",intervalMinutes:60,reuseWindowMinutes:10,enabled:true};
type FormValue = typeof emptyForm;

export default function SourcesRoute(){
  const initial=useLoaderData<typeof loader>();
  const [sources,setSources]=useState(initial.sources);
  const [editing,setEditing]=useState<number|null>(null);
  const [form,setForm]=useState<FormValue>(emptyForm);
  const [busy,setBusy]=useState(false);
  const [error,setError]=useState("");
  const [message,setMessage]=useState("");
  const [fetching,setFetching]=useState<number|null>(null);
  const [fetchResults,setFetchResults]=useState<Record<number,FetchResult>>({});

  function beginEdit(source:Subscription){setEditing(source.id);setForm({name:source.name,url:source.url,intervalMinutes:source.intervalMinutes,reuseWindowMinutes:source.reuseWindowMinutes,enabled:source.enabled});setError("");setMessage("");}
  function reset(){setEditing(null);setForm(emptyForm);setError("");setMessage("");}

  async function save(event:FormEvent<HTMLFormElement>){
    event.preventDefault();
    if((form.intervalMinutes<=30||form.reuseWindowMinutes<=5)&&!window.confirm("这会加大服务器压力，确定要这么做吗？"))return;
    setBusy(true);setError("");setMessage("");
    try{
      const response=await fetch(editing?`/api/me/sources/${editing}`:"/api/me/sources",{method:editing?"PUT":"POST",headers:{"Content-Type":"application/json","X-CSRF-Token":initial.csrfToken},body:JSON.stringify(form)});
      const body=await response.json() as Subscription&{error?:string};if(!response.ok)throw new Error(body.error||"保存信息源失败");
      setSources(rows=>editing?rows.map(row=>row.id===body.id?body:row):[body,...rows.filter(row=>row.id!==body.id)]);setMessage("信息源设置已保存。新文章会按你的关注项筛选；符合条件的近期内容可能被其他订阅者提前抓取。");
      setEditing(body.id);setForm({name:body.name,url:body.url,intervalMinutes:body.intervalMinutes,reuseWindowMinutes:body.reuseWindowMinutes,enabled:body.enabled});
    }catch(e){setError(e instanceof Error?e.message:"保存信息源失败");}finally{setBusy(false);}
  }

  async function toggle(source:Subscription){
    setBusy(true);setError("");try{
      const body={name:source.name,url:source.url,intervalMinutes:source.intervalMinutes,reuseWindowMinutes:source.reuseWindowMinutes,enabled:!source.enabled};
      const response=await fetch(`/api/me/sources/${source.id}`,{method:"PATCH",headers:{"Content-Type":"application/json","X-CSRF-Token":initial.csrfToken},body:JSON.stringify(body)});
      const result=await response.json() as Subscription&{error?:string};if(!response.ok)throw new Error(result.error||"更新订阅失败");setSources(rows=>rows.map(row=>row.id===result.id?result:row));if(editing===result.id){setForm({...form,enabled:result.enabled});}setMessage(result.enabled?"订阅已恢复。":"订阅已暂停。");
    }catch(e){setError(e instanceof Error?e.message:"更新订阅失败");}finally{setBusy(false);}
  }

  async function remove(source:Subscription){
    if(!window.confirm(`删除“${source.name}”的订阅吗？历史文章和个人结果会保留。`))return;
    setBusy(true);setError("");try{const response=await fetch(`/api/me/sources/${source.id}`,{method:"DELETE",headers:{"X-CSRF-Token":initial.csrfToken}});if(!response.ok)throw new Error("删除订阅失败");setSources(rows=>rows.filter(row=>row.id!==source.id));if(editing===source.id)reset();setMessage("订阅已删除，历史内容已保留。");}catch(e){setError(e instanceof Error?e.message:"删除订阅失败");}finally{setBusy(false);}
  }

  async function testFetch(source:Subscription){
    setFetching(source.id);setError("");setFetchResults(rows=>({...rows,[source.id]:{jobId:0,status:"queued",checkedAt:null,foundCount:0,newCount:0,items:[]}}));
    try{
      const response=await fetch(`/api/me/sources/${source.id}/fetch`,{method:"POST",headers:{"X-CSRF-Token":initial.csrfToken}});const queued=await response.json() as {jobId?:number;status?:string;error?:string};
      if(!response.ok){if(queued.status==="disabled")throw new Error("采集功能当前已关闭，无法执行测试抓取。");throw new Error(queued.error||"测试抓取未能排队");}
      if(!queued.jobId)throw new Error("任务没有返回编号");
      let latest:FetchResult|undefined;
      for(let attempt=0;attempt<45;attempt++){
        await new Promise(resolve=>setTimeout(resolve,1000));const poll=await fetch(`/api/me/sources/${source.id}/fetch/${queued.jobId}`,{headers:{"Accept":"application/json"}});const result=await poll.json() as FetchResult&{error?:string};if(!poll.ok)throw new Error(result.error||"读取抓取状态失败");latest=result;setFetchResults(rows=>({...rows,[source.id]:result}));if(["succeeded","reused","failed"].includes(result.status))break;
      }
      if(latest?.status==="failed")throw new Error(latest.error||"信源抓取失败");
    }catch(e){setError(e instanceof Error?e.message:"测试抓取失败");}finally{setFetching(null);}
  }

  return <main className="mx-auto max-w-[900px] pb-12">
    <Link to="/" className="text-[12px] text-ink-4 hover:text-accent">← 我的热点</Link>
    <div className="mt-3 flex flex-wrap items-end justify-between gap-3"><div><h1 className="text-[25px] font-semibold tracking-tight text-ink">我的信源</h1><p className="mt-2 max-w-[680px] text-[13px] leading-relaxed text-ink-3">添加 RSS / Atom 地址并设置抓取节奏。相同地址共用一次抓取，个人名称和订阅设置彼此独立；其他订阅者促成的新内容也可能提前出现。</p></div><span className="mono text-[11px] text-ink-4">{sources.length} 个订阅</span></div>

    <form onSubmit={save} className="card mt-6 p-5 sm:p-6">
      <div className="flex items-center justify-between gap-3"><h2 className="text-[15px] font-semibold text-ink">{editing?"编辑订阅":"添加 RSS / Atom"}</h2>{editing&&<button type="button" onClick={reset} className="text-[12px] text-ink-4 hover:text-accent">新建订阅</button>}</div>
      <div className="mt-4 grid gap-4 sm:grid-cols-2"><label className="text-[12px] font-medium text-ink-2">显示名称<input className={`${fieldClass} mt-1.5`} required maxLength={200} value={form.name} onChange={e=>setForm({...form,name:e.target.value})} placeholder="例如：技术博客"/></label><label className="text-[12px] font-medium text-ink-2 sm:col-span-1">RSS / Atom 地址<input className={`${fieldClass} mt-1.5`} required type="url" maxLength={2048} value={form.url} onChange={e=>setForm({...form,url:e.target.value})} placeholder="https://example.com/feed.xml"/></label>
        <label className="text-[12px] font-medium text-ink-2">抓取间隔（分钟）<input className={`${fieldClass} mt-1.5`} required type="number" min={1} max={1440} value={form.intervalMinutes} onChange={e=>setForm({...form,intervalMinutes:Number(e.target.value)})}/><span className="mt-1 block text-[11px] text-ink-4">默认 60 分钟；不超过 30 分钟会提示服务器压力。</span></label>
        <label className="text-[12px] font-medium text-ink-2">复用窗口（分钟）<input className={`${fieldClass} mt-1.5`} required type="number" min={1} max={1440} value={form.reuseWindowMinutes} onChange={e=>setForm({...form,reuseWindowMinutes:Number(e.target.value)})}/><span className="mt-1 block text-[11px] text-ink-4">默认 10 分钟；不超过 5 分钟会提示服务器压力。</span></label></div>
      {error&&<FormMessage error>{error}</FormMessage>}{message&&<FormMessage>{message}</FormMessage>}
      <button className="mt-5 h-10 rounded-full bg-accent px-5 text-[13px] font-semibold text-accent-contrast hover:bg-accent-ink disabled:opacity-50" disabled={busy}>{busy?"正在保存…":editing?"保存修改":"添加信源"}</button>
    </form>

    <section className="mt-8"><h2 className="text-[15px] font-semibold text-ink">已订阅信源</h2>{sources.length===0?<div className="card mt-3 border-dashed p-6 text-center text-[13px] text-ink-4">还没有订阅。添加 RSS 地址后，可在这里测试抓取。</div>:<div className="mt-3 grid gap-3">{sources.map(source=>{
      const result=fetchResults[source.id];return <article className="card p-4 sm:p-5" key={source.id}>
        <div className="flex flex-wrap items-start justify-between gap-3"><div className="min-w-0"><div className="flex flex-wrap items-center gap-2"><h3 className="text-[14px] font-semibold text-ink">{source.name}</h3><span className={`rounded-full px-2 py-0.5 text-[10px] ${source.enabled?"bg-accent-soft text-accent":"bg-bg-sunk text-ink-4"}`}>{source.enabled?"订阅中":"已暂停"}</span></div><a className="mt-1 block max-w-full truncate text-[11px] text-ink-4 hover:text-accent" href={source.url} target="_blank" rel="noreferrer">{source.url}</a><p className="mt-2 text-[11px] text-ink-3">你的设置：每 {source.intervalMinutes} 分钟 · 复用 {source.reuseWindowMinutes} 分钟；当前有效：每 {source.effectiveIntervalMinutes} 分钟 · 复用 {source.effectiveReuseWindowMinutes} 分钟</p><p className="mt-1 text-[10.5px] text-ink-4">最近检查：{source.lastCheckedAt?new Date(source.lastCheckedAt).toLocaleString():"尚未成功检查"}{source.nextFetchAt?` · 下次自动抓取 ${new Date(source.nextFetchAt).toLocaleString()}`:""}</p></div>
          <div className="flex shrink-0 flex-wrap gap-2"><button type="button" disabled={busy||fetching===source.id} onClick={()=>void testFetch(source)} className="h-8 rounded-full bg-accent px-3 text-[11px] font-semibold text-accent-contrast disabled:opacity-50">{fetching===source.id?"正在测试…":"测试抓取"}</button><button type="button" disabled={busy} onClick={()=>beginEdit(source)} className="h-8 rounded-full border border-line px-3 text-[11px] text-ink-2 hover:border-accent/40">编辑</button><button type="button" disabled={busy} onClick={()=>void toggle(source)} className="h-8 rounded-full border border-line px-3 text-[11px] text-ink-2 hover:border-accent/40">{source.enabled?"暂停":"恢复"}</button><button type="button" disabled={busy} onClick={()=>void remove(source)} className="h-8 rounded-full border border-line px-3 text-[11px] text-ink-4 hover:border-red-300 hover:text-red-600">删除</button></div>
        </div>
        {result&&<div className="mt-4 rounded-control bg-bg-sunk p-3 text-[11px] text-ink-3"><p>{result.status==="queued"?"抓取已排队":result.status==="running"?"正在抓取":result.status==="reused"?"复用了近期成功结果":result.status==="failed"?`抓取失败：${result.error??"未知错误"}`:`检查完成${result.checkedAt?` · ${new Date(result.checkedAt).toLocaleString()}`:""} · 读取 ${result.foundCount} 条 · 新增 ${result.newCount} 条`}</p>{result.items?.length>0&&<ul className="mt-2 space-y-1.5">{result.items.slice(0,20).map((item,index)=><li key={`${item.url}-${index}`}><a className="line-clamp-1 hover:text-accent" href={item.url} target="_blank" rel="noreferrer">{item.title}</a></li>)}</ul>}</div>}
      </article>})}</div>}</section>
  </main>;
}
