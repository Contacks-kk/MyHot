import type { Screen } from "../components/shell/screens";
import { Link, useLoaderData } from "react-router";
import type { Route } from "./+types/user-history";
import { userApiGet } from "../lib/user-api.server";

export const handle: Screen = { tab: "me", name: "历史归档" };

export async function loader({request}:Route.LoaderArgs){return userApiGet<{items:{articleId:string;score:number;title:string;summary:string;url:string;source:string;historical:boolean;createdAt:string}[];readOnly:boolean}>(request,"/api/me/history");}
export const meta: Route.MetaFunction = () => [{ title: "旧站历史归档 · MyHOT" }, { name: "robots", content: "noindex, nofollow" }];

export const headers:Route.HeadersFunction=()=>({"Cache-Control":"private, no-store","X-Robots-Tag":"noindex, nofollow"});
export default function UserHistory(){const {items}=useLoaderData<typeof loader>();return <section className="pb-10"><Link to="/" className="text-[12px] text-ink-4 hover:text-accent">← 我的热点</Link><h1 className="mt-4 text-[25px] font-semibold tracking-tight text-ink">旧站历史精选</h1><p className="mb-5 mt-2 text-[12px] text-ink-4">这是原站保留的只读历史分析，不会自动导入个人模型结果。</p><div className="divide-y divide-line rounded-card border border-line bg-surface px-4">{items.map(item=><article key={item.articleId} className="py-4"><div className="flex justify-between gap-3"><h2 className="text-[14px] font-semibold text-ink">{item.title}</h2><span className="mono text-[10px] text-ink-4">{item.score}</span></div><p className="mt-2 text-[12.5px] leading-relaxed text-ink-3">{item.summary}</p><p className="mt-2 text-[11px] text-ink-4">{item.source} · 历史只读</p><a href={item.url} target="_blank" rel="noopener noreferrer" className="mt-2 inline-block text-[11.5px] text-accent">查看原文 ↗</a></article>)}{!items.length&&<p className="py-10 text-center text-[13px] text-ink-4">没有可显示的历史内容。</p>}</div></section>;}
