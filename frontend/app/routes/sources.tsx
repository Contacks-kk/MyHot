import { useState, type FormEvent } from "react";
import { Link, useLoaderData } from "react-router";
import type { Route } from "./+types/sources";
import type { Screen } from "../components/shell/screens";
import { FormMessage, fieldClass } from "../features/account/common";
import { userApiGet, userApiMe } from "../lib/user-api.server";
import { useLocalePreference } from "../lib/local-state";

export const handle: Screen = { tab: "me", name: "我的信源" };

type Subscription = {
  id: number; sourceId: string; kind?: string; name: string; url: string;
  itemSelector?: string; titleSelector?: string; enabled: boolean;
  intervalMinutes: number; reuseWindowMinutes: number;
  effectiveIntervalMinutes: number; effectiveReuseWindowMinutes: number;
  lastCheckedAt: string | null; nextFetchAt: string | null; health: string;
};

type FetchResult = {
  jobId: number; status: "queued" | "running" | "succeeded" | "reused" | "failed" | string;
  checkedAt: string | null; foundCount: number; newCount: number; error?: string;
  items: Array<{title:string;url:string;publishedAt:string|null}>;
};

type DiscoverCandidate = {
  type: string;
  title: string;
  url: string;
  recommended: boolean;
};

type DiscoverResponse = {
  status: "found" | "not_found";
  originalUrl: string;
  candidates: DiscoverCandidate[];
  error?: string;
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

type SourceKindTab = "rss" | "web_list" | "ingest";

const emptyForm = {
  kind: "rss" as "rss" | "web_list",
  name: "",
  url: "",
  itemSelector: "",
  titleSelector: "",
  intervalMinutes: 60,
  reuseWindowMinutes: 10,
  enabled: true,
};
type FormValue = typeof emptyForm;

const emptyIngestForm = {
  url: "",
  title: "",
  text: "",
  author: "",
};
type IngestFormValue = typeof emptyIngestForm;

export default function SourcesRoute(){
  const initial=useLoaderData<typeof loader>();
  const locale = useLocalePreference();
  const [sources,setSources]=useState(initial.sources);
  const [activeTab, setActiveTab]=useState<SourceKindTab>("rss");
  const [editing,setEditing]=useState<number|null>(null);
  const [form,setForm]=useState<FormValue>(emptyForm);
  const [showAdvancedWeb, setShowAdvancedWeb]=useState(false);
  const [busy,setBusy]=useState(false);
  const [error,setError]=useState("");
  const [message,setMessage]=useState("");
  const [fetching,setFetching]=useState<number|null>(null);
  const [fetchResults,setFetchResults]=useState<Record<number,FetchResult>>({});

  const [discovering, setDiscovering] = useState(false);
  const [discoverResult, setDiscoverResult] = useState<DiscoverCandidate[] | null>(null);
  const [discoverNotice, setDiscoverNotice] = useState("");

  const [ingestForm, setIngestForm]=useState<IngestFormValue>(emptyIngestForm);
  const [ingestBusy, setIngestBusy]=useState(false);
  const [ingestMsg, setIngestMsg]=useState("");
  const [ingestErr, setIngestErr]=useState("");

  function beginEdit(source:Subscription){
    setEditing(source.id);
    const kind = source.kind === "web_list" ? "web_list" : "rss";
    setActiveTab(kind);
    setForm({
      kind,
      name: source.name,
      url: source.url,
      itemSelector: source.itemSelector || "",
      titleSelector: source.titleSelector || "",
      intervalMinutes: source.intervalMinutes,
      reuseWindowMinutes: source.reuseWindowMinutes,
      enabled: source.enabled,
    });
    setShowAdvancedWeb(Boolean(source.itemSelector || source.titleSelector));
    setDiscoverResult(null);
    setDiscoverNotice("");
    setError("");
    setMessage("");
  }

  function reset(){
    setEditing(null);
    setForm(emptyForm);
    setShowAdvancedWeb(false);
    setDiscoverResult(null);
    setDiscoverNotice("");
    setError("");
    setMessage("");
  }

  function switchTab(tab: SourceKindTab) {
    if (editing) reset();
    setActiveTab(tab);
    setForm(prev => ({ ...prev, kind: tab === "web_list" ? "web_list" : "rss" }));
    setError("");
    setMessage("");
    setIngestMsg("");
    setIngestErr("");
  }

  async function handleDiscover() {
    const target = form.url.trim();
    if (!target) {
      setError(locale === "en" ? "Please enter a website or feed URL" : "请输入需要探测的网址或 Feed 地址");
      return;
    }
    setDiscovering(true);
    setError("");
    setDiscoverNotice("");
    try {
      const response = await fetch("/api/me/sources/discover", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": initial.csrfToken,
        },
        body: JSON.stringify({ url: target }),
      });
      const data = await response.json() as DiscoverResponse;
      if (!response.ok) {
        throw new Error(data.error || (locale === "en" ? "Discovery failed" : "探测失败"));
      }
      if (data.status === "found" && data.candidates?.length > 0) {
        setDiscoverResult(data.candidates);
        setDiscoverNotice(
          locale === "en"
            ? `Found ${data.candidates.length} candidate feed(s). Click to apply:`
            : `成功探测到 ${data.candidates.length} 个可用信源，点击即可直接填入：`
        );
      } else {
        setDiscoverResult([]);
        setDiscoverNotice(
          locale === "en"
            ? "No public feed or sitemap detected. You can subscribe directly via 'Web List Crawler' instead!"
            : "未自动探测到公开 Feed 或 Sitemap。建议切换至【网页列表抓取】标签页直接订阅！"
        );
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : (locale === "en" ? "Discovery failed" : "探测失败"));
    } finally {
      setDiscovering(false);
    }
  }

  function applyCandidate(candidate: DiscoverCandidate) {
    setForm(prev => ({
      ...prev,
      kind: "rss",
      url: candidate.url,
      name: prev.name.trim() === "" ? candidate.title : prev.name,
    }));
    setActiveTab("rss");
    setDiscoverNotice(
      locale === "en"
        ? `Applied: ${candidate.title}`
        : `已填入：${candidate.title}`
    );
  }

  async function save(event:FormEvent<HTMLFormElement>){
    event.preventDefault();
    if((form.intervalMinutes<=30||form.reuseWindowMinutes<=5)&&!window.confirm(locale === "en" ? "High frequency increases server load. Confirm?" : "这会加大服务器压力，确定要这么做吗？"))return;
    setBusy(true);setError("");setMessage("");
    try{
      const payload = {
        kind: form.kind,
        name: form.name.trim(),
        url: form.url.trim(),
        itemSelector: form.kind === "web_list" ? form.itemSelector.trim() : "",
        titleSelector: form.kind === "web_list" ? form.titleSelector.trim() : "",
        intervalMinutes: form.intervalMinutes,
        reuseWindowMinutes: form.reuseWindowMinutes,
        enabled: form.enabled,
      };
      const response=await fetch(editing?`/api/me/sources/${editing}`:"/api/me/sources",{method:editing?"PUT":"POST",headers:{"Content-Type":"application/json","X-CSRF-Token":initial.csrfToken},body:JSON.stringify(payload)});
      const body=await response.json() as Subscription&{error?:string};if(!response.ok)throw new Error(body.error||(locale === "en" ? "Failed to save source" : "保存信息源失败"));
      setSources(rows=>editing?rows.map(row=>row.id===body.id?body:row):[body,...rows.filter(row=>row.id!==body.id)]);
      setMessage(locale === "en" ? "Source settings saved." : "信息源设置已保存。新文章会按你的关注项筛选；符合条件的近期内容可能被其他订阅者提前抓取。");
      setEditing(body.id);
      setDiscoverResult(null);
      setDiscoverNotice("");
    }catch(e){setError(e instanceof Error?e.message:(locale === "en" ? "Failed to save source" : "保存信息源失败"));}finally{setBusy(false);}
  }

  async function handleIngest(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!ingestForm.url.trim()) return;
    setIngestBusy(true);
    setIngestMsg("");
    setIngestErr("");
    try {
      const response = await fetch("/api/me/ingest", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": initial.csrfToken,
        },
        body: JSON.stringify({
          url: ingestForm.url.trim(),
          title: ingestForm.title.trim(),
          text: ingestForm.text.trim(),
          author: ingestForm.author.trim(),
        }),
      });
      const data = await response.json() as { ok?: boolean; error?: string; created?: boolean };
      if (!response.ok) {
        throw new Error(data.error || (locale === "en" ? "Failed to ingest article" : "文章投递失败"));
      }
      setIngestMsg(
        locale === "en"
          ? "Article submitted successfully! It is now queued for AI summarization and scoring."
          : "文章投递成功！已加入你的专属处理队列，系统将自动抓取正文并进行 AI 摘要与热点评分。"
      );
      setIngestForm(emptyIngestForm);
      // Refresh source list
      const res = await fetch("/api/me/sources");
      if (res.ok) {
        const body = await res.json() as { sources?: Subscription[] };
        if (body.sources) setSources(body.sources);
      }
    } catch (e) {
      setIngestErr(e instanceof Error ? e.message : (locale === "en" ? "Ingest failed" : "投递失败"));
    } finally {
      setIngestBusy(false);
    }
  }

  async function toggle(source:Subscription){
    setBusy(true);setError("");try{
      const body={name:source.name,url:source.url,intervalMinutes:source.intervalMinutes,reuseWindowMinutes:source.reuseWindowMinutes,enabled:!source.enabled};
      const response=await fetch(`/api/me/sources/${source.id}`,{method:"PATCH",headers:{"Content-Type":"application/json","X-CSRF-Token":initial.csrfToken},body:JSON.stringify(body)});
      const result=await response.json() as Subscription&{error?:string};if(!response.ok)throw new Error(result.error||(locale === "en" ? "Failed to update subscription" : "更新订阅失败"));setSources(rows=>rows.map(row=>row.id===result.id?result:row));if(editing===result.id){setForm({...form,enabled:result.enabled});}setMessage(result.enabled?(locale === "en" ? "Subscription resumed." : "订阅已恢复。"):(locale === "en" ? "Subscription paused." : "订阅已暂停。"));
    }catch(e){setError(e instanceof Error?e.message:(locale === "en" ? "Failed to update subscription" : "更新订阅失败"));}finally{setBusy(false);}
  }

  async function remove(source:Subscription){
    if(!window.confirm(locale === "en" ? `Delete subscription for "${source.name}"?` : `删除“${source.name}”的订阅吗？历史文章和个人结果会保留。`))return;
    setBusy(true);setError("");try{const response=await fetch(`/api/me/sources/${source.id}`,{method:"DELETE",headers:{"X-CSRF-Token":initial.csrfToken}});if(!response.ok)throw new Error(locale === "en" ? "Failed to delete subscription" : "删除订阅失败");setSources(rows=>rows.filter(row=>row.id!==source.id));if(editing===source.id)reset();setMessage(locale === "en" ? "Subscription deleted." : "订阅已删除，历史内容已保留。");}catch(e){setError(e instanceof Error?e.message:(locale === "en" ? "Failed to delete subscription" : "删除订阅失败"));}finally{setBusy(false);}
  }

  async function testFetch(source:Subscription){
    setFetching(source.id);setError("");setFetchResults(rows=>({...rows,[source.id]:{jobId:0,status:"queued",checkedAt:null,foundCount:0,newCount:0,items:[]}}));
    try{
      const response=await fetch(`/api/me/sources/${source.id}/fetch`,{method:"POST",headers:{"X-CSRF-Token":initial.csrfToken}});const queued=await response.json() as {jobId?:number;status?:string;error?:string};
      if(!response.ok){if(queued.status==="disabled")throw new Error(locale === "en" ? "Collection is currently disabled." : "采集功能当前已关闭，无法执行测试抓取。");throw new Error(queued.error||(locale === "en" ? "Test fetch could not be queued" : "测试抓取未能排队"));}
      if(!queued.jobId)throw new Error(locale === "en" ? "Job ID missing" : "任务没有返回编号");
      let latest:FetchResult|undefined;
      for(let attempt=0;attempt<45;attempt++){
        await new Promise(resolve=>setTimeout(resolve,1000));const poll=await fetch(`/api/me/sources/${source.id}/fetch/${queued.jobId}`,{headers:{"Accept":"application/json"}});const result=await poll.json() as FetchResult&{error?:string};if(!poll.ok)throw new Error(result.error||(locale === "en" ? "Failed to poll fetch status" : "读取抓取状态失败"));latest=result;setFetchResults(rows=>({...rows,[source.id]:result}));if(["succeeded","reused","failed"].includes(result.status))break;
      }
      if(latest?.status==="failed")throw new Error(latest.error||(locale === "en" ? "Source fetch failed" : "信源抓取失败"));
    }catch(e){setError(e instanceof Error?e.message:(locale === "en" ? "Test fetch failed" : "测试抓取失败"));}finally{setFetching(null);}
  }

  return <main className="mx-auto max-w-[900px] pb-12">
    <Link to="/" className="text-[12px] text-ink-4 hover:text-accent">← {locale === "en" ? "My Hot" : "我的热点"}</Link>
    <div className="mt-3 flex flex-wrap items-end justify-between gap-3">
      <div>
        <h1 className="text-[25px] font-semibold tracking-tight text-ink">{locale === "en" ? "My Sources" : "我的信源"}</h1>
        <p className="mt-2 max-w-[680px] text-[13px] leading-relaxed text-ink-3">
          {locale === "en"
            ? "Support multiple source types: standard RSS/Atom feeds, Web List Crawlers (no RSS required), and direct single-article Ingest Webhooks."
            : "支持多种信源类型：传统 RSS/Atom 订阅、网页列表直接抓取（无需 RSS）、以及单篇快速投递 / Webhook。"}
        </p>
      </div>
      <span className="mono text-[11px] text-ink-4">{sources.length} {locale === "en" ? "subscriptions" : "个信源"}</span>
    </div>

    {/* Source Type Navigation Tabs */}
    <div className="mt-6 flex border-b border-line gap-2">
      <button
        type="button"
        onClick={() => switchTab("rss")}
        className={`pb-3 px-3 text-[13px] font-semibold border-b-2 transition-colors ${
          activeTab === "rss"
            ? "border-accent text-accent"
            : "border-transparent text-ink-3 hover:text-ink"
        }`}
      >
        {locale === "en" ? "RSS / Atom Feed" : "RSS / Atom 订阅"}
      </button>
      <button
        type="button"
        onClick={() => switchTab("web_list")}
        className={`pb-3 px-3 text-[13px] font-semibold border-b-2 transition-colors ${
          activeTab === "web_list"
            ? "border-accent text-accent"
            : "border-transparent text-ink-3 hover:text-ink"
        }`}
      >
        <span className="flex items-center gap-1.5">
          {locale === "en" ? "Web List Crawler" : "网页列表抓取（无需 RSS）"}
          <span className="rounded-full bg-accent/15 px-1.5 py-0.2 text-[9px] font-bold text-accent uppercase">NEW</span>
        </span>
      </button>
      <button
        type="button"
        onClick={() => switchTab("ingest")}
        className={`pb-3 px-3 text-[13px] font-semibold border-b-2 transition-colors ${
          activeTab === "ingest"
            ? "border-accent text-accent"
            : "border-transparent text-ink-3 hover:text-ink"
        }`}
      >
        <span className="flex items-center gap-1.5">
          {locale === "en" ? "Quick Ingest & Webhook" : "单篇直接投递 / Webhook"}
          <span className="rounded-full bg-purple-500/15 px-1.5 py-0.2 text-[9px] font-bold text-purple-600 dark:text-purple-300 uppercase">NEW</span>
        </span>
      </button>
    </div>

    {/* Tab 1 & Tab 2: Regular Sources Form (RSS or Web List) */}
    {activeTab !== "ingest" && (
      <form onSubmit={save} className="card mt-6 p-5 sm:p-6">
        <div className="flex items-center justify-between gap-3">
          <h2 className="text-[15px] font-semibold text-ink">
            {editing
              ? (locale === "en" ? "Edit Source" : "编辑信源")
              : activeTab === "web_list"
              ? (locale === "en" ? "Add Web List Crawler" : "添加网页列表抓取（普通网页）")
              : (locale === "en" ? "Add RSS / Atom" : "添加 RSS / Atom 订阅")}
          </h2>
          {editing && <button type="button" onClick={reset} className="text-[12px] text-ink-4 hover:text-accent">{locale === "en" ? "New Subscription" : "新建订阅"}</button>}
        </div>

        {activeTab === "web_list" && (
          <p className="mt-2 text-[12px] text-ink-3 leading-relaxed">
            {locale === "en"
              ? "Directly crawl any blog archive, news list, or community index page without requiring an RSS feed. The system parses links and automatically updates content."
              : "直接抓取任意没有 RSS 的普通网站列表（如公司官网新闻页、个人博客目录、论坛板块），系统定时解析列表中的文章链接并入库。"}
          </p>
        )}

        <div className="mt-4 grid gap-4 sm:grid-cols-2">
          <label className="text-[12px] font-medium text-ink-2">
            {locale === "en" ? "Display Name" : "显示名称"}
            <input className={`${fieldClass} mt-1.5`} required maxLength={200} value={form.name} onChange={e=>setForm({...form,name:e.target.value})} placeholder={activeTab === "web_list" ? (locale === "en" ? "e.g. OpenAI News" : "例如：OpenAI 官方动态") : (locale === "en" ? "e.g. Tech Blog" : "例如：技术博客")}/>
          </label>

          <div className="sm:col-span-1">
            <label className="text-[12px] font-medium text-ink-2 block">
              {activeTab === "web_list"
                ? (locale === "en" ? "Web Page List URL" : "网页列表地址（HTML 页面）")
                : (locale === "en" ? "RSS / Feed URL" : "信源地址（RSS / Atom / 网站）")}
            </label>
            <div className="mt-1.5 flex gap-2">
              <input
                className={`${fieldClass} flex-1`}
                required
                type="url"
                maxLength={2048}
                value={form.url}
                onChange={e=>{
                  setForm({...form,url:e.target.value});
                  if(discoverResult)setDiscoverResult(null);
                }}
                placeholder={activeTab === "web_list" ? "https://openai.com/news 或 https://example.com/blog" : "https://example.com/feed.xml"}
              />
              {activeTab === "rss" && (
                <button
                  type="button"
                  disabled={discovering || busy || !form.url.trim()}
                  onClick={handleDiscover}
                  className="h-10 shrink-0 rounded-control border border-line bg-bg-card px-3.5 text-[12px] font-medium text-ink-2 hover:border-accent hover:text-accent disabled:opacity-40 transition-colors"
                >
                  {discovering ? (locale === "en" ? "Probing..." : "探测中…") : (locale === "en" ? "Auto-Discover" : "智能探测")}
                </button>
              )}
            </div>
          </div>

          {/* RSS Candidate Discovery Results */}
          {discoverResult && discoverResult.length > 0 && (
            <div className="sm:col-span-2 rounded-control border border-accent/20 bg-accent-soft/30 p-3">
              <div className="flex items-center justify-between text-[11.5px] font-medium text-accent">
                <span>{discoverNotice}</span>
                <button
                  type="button"
                  onClick={() => setDiscoverResult(null)}
                  className="text-ink-4 hover:text-ink text-[12px] px-1"
                  title={locale === "en" ? "Close" : "关闭"}
                >
                  ✕
                </button>
              </div>
              <div className="mt-2 flex flex-wrap gap-2">
                {discoverResult.map((c, idx) => (
                  <button
                    key={`${c.url}-${idx}`}
                    type="button"
                    onClick={() => applyCandidate(c)}
                    className="group flex items-center gap-1.5 rounded-full border border-line bg-bg-card px-3 py-1 text-[11px] text-ink-2 hover:border-accent hover:text-accent shadow-xs transition-colors"
                  >
                    <span className={`rounded-full px-1.5 py-0.2 text-[9.5px] font-semibold uppercase ${
                      c.type === "sitemap"
                        ? "bg-purple-100 text-purple-700 dark:bg-purple-950 dark:text-purple-300"
                        : c.type === "atom"
                        ? "bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300"
                        : "bg-accent-soft text-accent"
                    }`}>
                      {c.type}
                    </span>
                    <span className="font-medium max-w-[260px] truncate">{c.title || c.url}</span>
                    {c.recommended && <span className="text-[10px] text-amber-500 font-bold" title={locale === "en" ? "Recommended" : "推荐"}>★</span>}
                  </button>
                ))}
              </div>
            </div>
          )}

          {discoverResult && discoverResult.length === 0 && (
            <div className="sm:col-span-2 rounded-control bg-bg-sunk p-2.5 text-[11px] text-ink-4 flex items-center justify-between">
              <span>{discoverNotice}</span>
              <button type="button" onClick={() => setDiscoverResult(null)} className="text-ink-4 hover:text-ink text-[12px] px-1">✕</button>
            </div>
          )}

          {/* Web List Advanced CSS Selectors */}
          {activeTab === "web_list" && (
            <div className="sm:col-span-2">
              <button
                type="button"
                onClick={() => setShowAdvancedWeb(!showAdvancedWeb)}
                className="text-[11.5px] font-medium text-accent hover:underline flex items-center gap-1"
              >
                <span>{showAdvancedWeb ? "▼" : "▶"}</span>
                <span>{locale === "en" ? "Advanced CSS Selectors (Optional)" : "高级 CSS 选择器设置（可选）"}</span>
              </button>
              {showAdvancedWeb && (
                <div className="mt-3 grid gap-3 sm:grid-cols-2 rounded-control border border-line bg-bg-sunk/40 p-3.5">
                  <label className="text-[11.5px] font-medium text-ink-2">
                    {locale === "en" ? "Item / Container Selector" : "文章项选择器（itemSelector）"}
                    <input
                      className={`${fieldClass} mt-1`}
                      value={form.itemSelector}
                      onChange={e=>setForm({...form,itemSelector:e.target.value})}
                      placeholder="默认自动识别所有文章链接，如 article, .post"
                    />
                    <span className="mt-1 block text-[10.5px] text-ink-4">留空时自动提取页面内部文章超链接。</span>
                  </label>
                  <label className="text-[11.5px] font-medium text-ink-2">
                    {locale === "en" ? "Title Selector" : "标题选择器（titleSelector）"}
                    <input
                      className={`${fieldClass} mt-1`}
                      value={form.titleSelector}
                      onChange={e=>setForm({...form,titleSelector:e.target.value})}
                      placeholder="可选，例如 h2, h3, .title"
                    />
                    <span className="mt-1 block text-[10.5px] text-ink-4">留空时使用超链接文字作为标题。</span>
                  </label>
                </div>
              )}
            </div>
          )}

          <label className="text-[12px] font-medium text-ink-2">
            {locale === "en" ? "Interval (Minutes)" : "抓取间隔（分钟）"}
            <input className={`${fieldClass} mt-1.5`} required type="number" min={1} max={1440} value={form.intervalMinutes} onChange={e=>setForm({...form,intervalMinutes:Number(e.target.value)})}/>
            <span className="mt-1 block text-[11px] text-ink-4">{locale === "en" ? "Default 60m; ≤30m prompts warning." : "默认 60 分钟；不超过 30 分钟会提示服务器压力。"}</span>
          </label>
          <label className="text-[12px] font-medium text-ink-2">
            {locale === "en" ? "Reuse Window (Minutes)" : "复用窗口（分钟）"}
            <input className={`${fieldClass} mt-1.5`} required type="number" min={1} max={1440} value={form.reuseWindowMinutes} onChange={e=>setForm({...form,reuseWindowMinutes:Number(e.target.value)})}/><span className="mt-1 block text-[11px] text-ink-4">{locale === "en" ? "Default 10m; ≤5m prompts warning." : "默认 10 分钟；不超过 5 分钟会提示服务器压力。"}</span>
          </label>
        </div>

        {error&&<FormMessage error>{error}</FormMessage>}{message&&<FormMessage>{message}</FormMessage>}
        <button className="mt-5 h-10 rounded-full bg-accent px-5 text-[13px] font-semibold text-accent-contrast hover:bg-accent-ink disabled:opacity-50" disabled={busy}>
          {busy ? (locale === "en" ? "Saving..." : "正在保存…") : editing ? (locale === "en" ? "Save Changes" : "保存修改") : activeTab === "web_list" ? (locale === "en" ? "Add Web List" : "添加网页列表") : (locale === "en" ? "Add Source" : "添加信源")}
        </button>
      </form>
    )}

    {/* Tab 3: Quick Ingest & Webhook */}
    {activeTab === "ingest" && (
      <div className="mt-6 space-y-6">
        {/* Direct Ingest Form */}
        <form onSubmit={handleIngest} className="card p-5 sm:p-6">
          <h2 className="text-[15px] font-semibold text-ink">
            {locale === "en" ? "Quick Article Ingest" : "单篇直接投递到个人热点"}
          </h2>
          <p className="mt-2 text-[12px] text-ink-3 leading-relaxed">
            {locale === "en"
              ? "Paste any article link or content here. MyHOT will directly ingest it, run AI summarization, evaluate against your interests, and score it into your daily report."
              : "在网页上看到好文章？直接粘贴链接或正文投递到 MyHOT，系统将立即进行正文解析、AI 摘要、关键词打分并汇总至你的热点与日报。"}
          </p>

          <div className="mt-4 grid gap-4 sm:grid-cols-2">
            <label className="text-[12px] font-medium text-ink-2 sm:col-span-2">
              {locale === "en" ? "Article URL (Required)" : "文章原文链接（必填）"}
              <input
                className={`${fieldClass} mt-1.5`}
                required
                type="url"
                maxLength={2048}
                value={ingestForm.url}
                onChange={e=>setIngestForm({...ingestForm,url:e.target.value})}
                placeholder="https://example.com/great-article"
              />
            </label>

            <label className="text-[12px] font-medium text-ink-2">
              {locale === "en" ? "Article Title (Optional)" : "文章标题（可选）"}
              <input
                className={`${fieldClass} mt-1.5`}
                maxLength={500}
                value={ingestForm.title}
                onChange={e=>setIngestForm({...ingestForm,title:e.target.value})}
                placeholder={locale === "en" ? "Leave blank to auto-fetch from page" : "留空则自动从网页提取"}
              />
            </label>

            <label className="text-[12px] font-medium text-ink-2">
              {locale === "en" ? "Author (Optional)" : "作者（可选）"}
              <input
                className={`${fieldClass} mt-1.5`}
                maxLength={200}
                value={ingestForm.author}
                onChange={e=>setIngestForm({...ingestForm,author:e.target.value})}
                placeholder={locale === "en" ? "e.g. John Doe" : "例如：张三"}
              />
            </label>

            <label className="text-[12px] font-medium text-ink-2 sm:col-span-2">
              {locale === "en" ? "Text / Excerpt (Optional)" : "正文或摘要（可选）"}
              <textarea
                className={`${fieldClass} mt-1.5 h-24 py-2 text-[12px]`}
                value={ingestForm.text}
                onChange={e=>setIngestForm({...ingestForm,text:e.target.value})}
                placeholder={locale === "en" ? "Optional: paste full text or excerpt to bypass paywalls or anti-crawler protections." : "选填：可直接粘贴正文或摘要文本，规避防爬虫拦截。"}
              />
            </label>
          </div>

          {ingestErr && <FormMessage error>{ingestErr}</FormMessage>}
          {ingestMsg && <FormMessage>{ingestMsg}</FormMessage>}

          <button
            className="mt-5 h-10 rounded-full bg-accent px-5 text-[13px] font-semibold text-accent-contrast hover:bg-accent-ink disabled:opacity-50"
            disabled={ingestBusy}
          >
            {ingestBusy ? (locale === "en" ? "Ingesting..." : "正在投递…") : (locale === "en" ? "Submit to MyHOT" : "立即投递入库")}
          </button>
        </form>

        {/* Webhook & Automation Guide */}
        <div className="card p-5 sm:p-6 bg-bg-sunk/30">
          <h3 className="text-[14px] font-semibold text-ink flex items-center gap-2">
            <span>⚡</span>
            <span>{locale === "en" ? "Personal Ingest Webhook API" : "专属 Webhook 自动化接口"}</span>
          </h3>
          <p className="mt-2 text-[12px] text-ink-3 leading-relaxed">
            {locale === "en"
              ? "You can easily push articles from browser extensions, iOS Shortcuts, n8n, or Zapier using the standard HTTP POST endpoint:"
              : "你可以配合油猴剪藏脚本、iOS 快捷指令、Zapier 或 n8n 自动化流程，通过 HTTP POST 请求将任意文章一键推送到 MyHOT："}
          </p>

          <div className="mt-3 overflow-x-auto rounded-control bg-bg p-3.5 mono text-[11px] text-ink-2 border border-line">
            <pre className="whitespace-pre-wrap">{`curl -X POST "${typeof window !== 'undefined' ? window.location.origin : ''}/api/me/ingest" \\
  -H "Content-Type: application/json" \\
  -H "X-CSRF-Token: ${initial.csrfToken}" \\
  -d '{"url": "https://example.com/article", "title": "文章标题"}'`}</pre>
          </div>
          <p className="mt-2 text-[11px] text-ink-4">
            {locale === "en"
              ? "Tip: The system automatically deduplicates by URL. Discovered content is processed through your private model configuration."
              : "提示：相同 URL 会自动去重；入库后会自动按你的个人关键词筛选并调用你配置的模型生成 AI 摘要。"}
          </p>
        </div>
      </div>
    )}

    {/* Subscribed Sources List */}
    <section className="mt-8">
      <h2 className="text-[15px] font-semibold text-ink">{locale === "en" ? "Subscribed Sources" : "已订阅信源"}</h2>
      {sources.length===0?<div className="card mt-3 border-dashed p-6 text-center text-[13px] text-ink-4">{locale === "en" ? "No subscriptions yet. Add a website, feed, or web list above." : "还没有订阅。添加网址、Feed 或网页列表后，可在这里测试抓取。"}</div>:<div className="mt-3 grid gap-3">{sources.map(source=>{
        const result=fetchResults[source.id];
        const isWebList = source.kind === "web_list";
        const isIngest = source.kind === "external";
        return <article className="card p-4 sm:p-5" key={source.id}>
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                <h3 className="text-[14px] font-semibold text-ink">{source.name}</h3>
                {isWebList ? (
                  <span className="rounded-full bg-blue-100 text-blue-700 dark:bg-blue-950 dark:text-blue-300 px-2 py-0.5 text-[10px] font-semibold">
                    {locale === "en" ? "Web List" : "网页列表"}
                  </span>
                ) : isIngest ? (
                  <span className="rounded-full bg-purple-100 text-purple-700 dark:bg-purple-950 dark:text-purple-300 px-2 py-0.5 text-[10px] font-semibold">
                    {locale === "en" ? "Direct Ingest" : "手动投递"}
                  </span>
                ) : (
                  <span className="rounded-full bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300 px-2 py-0.5 text-[10px] font-semibold">
                    RSS / Atom
                  </span>
                )}
                <span className={`rounded-full px-2 py-0.5 text-[10px] ${source.enabled?"bg-accent-soft text-accent":"bg-bg-sunk text-ink-4"}`}>
                  {source.enabled?(locale === "en" ? "Active" : "订阅中"):(locale === "en" ? "Paused" : "已暂停")}
                </span>
              </div>
              <a className="mt-1 block max-w-full truncate text-[11px] text-ink-4 hover:text-accent" href={source.url} target="_blank" rel="noreferrer">{source.url}</a>
              {!isIngest && (
                <p className="mt-2 text-[11px] text-ink-3">
                  {locale === "en"
                    ? `Your setting: Every ${source.intervalMinutes}m · Reuse ${source.reuseWindowMinutes}m; Effective: Every ${source.effectiveIntervalMinutes}m · Reuse ${source.effectiveReuseWindowMinutes}m`
                    : `你的设置：每 ${source.intervalMinutes} 分钟 · 复用 ${source.reuseWindowMinutes} 分钟；当前有效：每 ${source.effectiveIntervalMinutes} 分钟 · 复用 ${source.effectiveReuseWindowMinutes} 分钟`}
                </p>
              )}
              <p className="mt-1 text-[10.5px] text-ink-4">
                {locale === "en"
                  ? `Last checked: ${source.lastCheckedAt ? new Date(source.lastCheckedAt).toLocaleString() : "Never"}${source.nextFetchAt ? ` · Next fetch: ${new Date(source.nextFetchAt).toLocaleString()}` : ""}`
                  : `最近检查：${source.lastCheckedAt ? new Date(source.lastCheckedAt).toLocaleString() : "尚未成功检查"}${source.nextFetchAt ? ` · 下次自动抓取 ${new Date(source.nextFetchAt).toLocaleString()}` : ""}`}
              </p>
            </div>
            <div className="flex shrink-0 flex-wrap gap-2">
              {!isIngest && (
                <button type="button" disabled={busy||fetching===source.id} onClick={()=>void testFetch(source)} className="h-8 rounded-full bg-accent px-3 text-[11px] font-semibold text-accent-contrast disabled:opacity-50">
                  {fetching===source.id?(locale === "en" ? "Testing..." : "正在测试…"):(locale === "en" ? "Test Fetch" : "测试抓取")}
                </button>
              )}
              {!isIngest && (
                <button type="button" disabled={busy} onClick={()=>beginEdit(source)} className="h-8 rounded-full border border-line px-3 text-[11px] text-ink-2 hover:border-accent/40">
                  {locale === "en" ? "Edit" : "编辑"}
                </button>
              )}
              <button type="button" disabled={busy} onClick={()=>void toggle(source)} className="h-8 rounded-full border border-line px-3 text-[11px] text-ink-2 hover:border-accent/40">
                {source.enabled?(locale === "en" ? "Pause" : "暂停"):(locale === "en" ? "Resume" : "恢复")}
              </button>
              <button type="button" disabled={busy} onClick={()=>void remove(source)} className="h-8 rounded-full border border-line px-3 text-[11px] text-ink-4 hover:border-red-300 hover:text-red-600">
                {locale === "en" ? "Delete" : "删除"}
              </button>
            </div>
          </div>
          {result&&<div className="mt-4 rounded-control bg-bg-sunk p-3 text-[11px] text-ink-3"><p>{result.status==="queued"?(locale === "en" ? "Fetch queued" : "抓取已排队"):result.status==="running"?(locale === "en" ? "Fetching..." : "正在抓取"):result.status==="reused"?(locale === "en" ? "Reused recent successful fetch" : "复用了近期成功结果"):result.status==="failed"?(locale === "en" ? `Fetch failed: ${result.error??"Unknown error"}` : `抓取失败：${result.error??"未知错误"}`):locale === "en" ? `Checked${result.checkedAt?` · ${new Date(result.checkedAt).toLocaleString()}`:""} · Found ${result.foundCount} · New ${result.newCount}` : `检查完成${result.checkedAt?` · ${new Date(result.checkedAt).toLocaleString()}`:""} · 读取 ${result.foundCount} 条 · 新增 ${result.newCount} 条`}</p>{result.items?.length>0&&<ul className="mt-2 space-y-1.5">{result.items.slice(0,20).map((item,index)=><li key={`${item.url}-${index}`}><a className="line-clamp-1 hover:text-accent" href={item.url} target="_blank" rel="noreferrer">{item.title}</a></li>)}</ul>}</div>}
        </article>})}</div>}
    </section>
  </main>;
}
