import type { Screen } from "../components/shell/screens";
import { useState, type FormEvent } from "react";
import { Link, useLoaderData } from "react-router";
import type { Route } from "./+types/interest-settings";
import { FormMessage, fieldClass } from "../features/account/common";
import { userApiGet, userApiMe } from "../lib/user-api.server";

export const handle: Screen = { tab: "me", name: "关注项" };

export async function loader({ request }: Route.LoaderArgs) {
  const [user, interests, catalog] = await Promise.all([
    userApiMe(request),
    userApiGet<{keywords:string[];entityIds:string[];version:number}>(request,"/api/me/interests"),
    userApiGet<{entities:{id:string;name:string}[]}>(request,"/api/me/entities"),
  ]);
  return { csrfToken:user?.csrfToken??"", ...interests, entities:catalog.entities };
}

export const meta: Route.MetaFunction = () => [{ title: "关注项 · MyHOT" }, { name: "robots", content: "noindex, nofollow" }];

export const headers: Route.HeadersFunction=()=>({"Cache-Control":"private, no-store","X-Robots-Tag":"noindex, nofollow"});

export default function InterestSettings(){
  const initial=useLoaderData<typeof loader>();const [keywords,setKeywords]=useState(initial.keywords.join("\n"));const [selected,setSelected]=useState(initial.entityIds);const [busy,setBusy]=useState(false);const [error,setError]=useState("");const [message,setMessage]=useState("");
  function toggle(id:string){setSelected(values=>values.includes(id)?values.filter(x=>x!==id):[...values,id]);}
  async function save(event:FormEvent<HTMLFormElement>){event.preventDefault();setBusy(true);setError("");setMessage("");try{const response=await fetch("/api/me/interests",{method:"PUT",headers:{"Content-Type":"application/json","X-CSRF-Token":initial.csrfToken},body:JSON.stringify({keywords:keywords.split(/[\n,，]/),entityIds:selected})});const result=await response.json() as {error?:string};if(!response.ok)throw new Error(result.error||"保存失败");setMessage("关注项已保存，新设置从后续采集文章开始生效。");}catch(e){setError(e instanceof Error?e.message:"保存失败");}finally{setBusy(false);}}
  return <main className="mx-auto max-w-[760px] pb-10"><Link to="/" className="text-[12px] text-ink-4 hover:text-accent">← 我的热点</Link><h1 className="mt-3 text-[25px] font-semibold tracking-tight text-ink">关注项</h1><p className="mt-2 text-[13px] leading-relaxed text-ink-3">命中任一关键词或公司实体，文章就会进入你的个人模型加工队列。修改关注项不会回填旧文章。</p>
    <form onSubmit={save} className="card mt-6 p-5 sm:p-6"><label htmlFor="keywords" className="text-[13px] font-semibold text-ink">关键词</label><p className="mt-1 text-[11.5px] text-ink-4">每行一个，也可用逗号分隔，最多 30 个。</p><textarea className={`${fieldClass} mt-2 min-h-32 resize-y py-3 leading-relaxed`} id="keywords" value={keywords} onChange={e=>setKeywords(e.target.value)} placeholder={'例如：\n具身智能\n芯片出口政策\n开源 Agent'} maxLength={4000}/>
      <div className="mt-6 flex items-end justify-between gap-3"><div><h2 className="text-[13px] font-semibold text-ink">公司与产品实体</h2><p className="mt-1 text-[11.5px] text-ink-4">从平台维护的名录中选择，最多 100 项。</p></div><span className="mono text-[11px] text-ink-4">{selected.length} 已选</span></div>
      <div className="mt-3 grid grid-cols-2 gap-2 sm:grid-cols-3">{initial.entities.map(entity=><label key={entity.id} className={`flex cursor-pointer items-center gap-2 rounded-control border px-3 py-2.5 text-[12.5px] transition-colors ${selected.includes(entity.id)?"border-accent/40 bg-accent-soft text-accent":"border-line bg-surface text-ink-2 hover:border-line-strong"}`}><input type="checkbox" checked={selected.includes(entity.id)} onChange={()=>toggle(entity.id)} className="accent-accent"/>{entity.name}</label>)}</div>
      {error&&<FormMessage error>{error}</FormMessage>}{message&&<FormMessage>{message}</FormMessage>}<button className="mt-6 h-10 rounded-full bg-accent px-5 text-[13px] font-semibold text-accent-contrast hover:bg-accent-ink disabled:opacity-50" disabled={busy}>{busy?"正在保存…":"保存关注项"}</button>
    </form>
  </main>;
}
