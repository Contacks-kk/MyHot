import { useEffect, useState } from "react";
import { data, Link, useLoaderData, useNavigate, useRevalidator } from "react-router";
import type { Screen } from "../components/shell/screens";
import type { Route } from "./+types/user-daily";
import { Halftone } from "../features/report/Halftone";
import { Kicker } from "../components/ui/Kicker";
import { MarkdownContent } from "../components/ui/MarkdownContent";
import { scrollToAnchor } from "../components/ui/OutlineSheet";
import { userApiGet, userApiMe } from "../lib/user-api.server";
import { postJSON } from "../features/account/common";

export const handle: Screen = { tab: "daily", name: "我的日报" };

interface DailyEntry {
  articleId: string;
  score: number;
  title: string;
  summary: string;
  url: string;
  source: string;
}

interface DailyReport {
  id: string;
  date: string;
  title: string;
  summary: string;
  entries: DailyEntry[] | null;
  createdAt: string;
}

/** Load the current user's reports and select the requested issue. */
export async function loader({ request }: Route.LoaderArgs) {
  const [result, user] = await Promise.all([userApiGet<{ reports: DailyReport[] }>(request, "/api/me/daily"), userApiMe(request)]);
  // The existing API can still serve scheduled issues during the backend upgrade.
  const reports = result.reports.map((item) => ({ ...item, id: item.id ?? `scheduled-${item.date}` }));
  const requestedId = new URL(request.url).searchParams.get("id");
  const requested = new URL(request.url).searchParams.get("date");
  const report = requestedId ? reports.find((item) => item.id === requestedId) : requested ? reports.find((item) => item.date === requested) : reports[0];
  if ((requestedId || requested) && !report) {
    throw data({ message: "日报不存在" }, { status: 404 });
  }
  return { reports, report: report ?? null, csrfToken: user?.csrfToken ?? "" };
}

export const meta: Route.MetaFunction = () => [{ title: "我的日报" }, { name: "robots", content: "noindex, nofollow" }];
export const headers: Route.HeadersFunction = () => ({ "Cache-Control": "private, no-store", "X-Robots-Tag": "noindex, nofollow" });

/** Format an issue date without depending on the browser's time zone. */
function formatDate(date: string) {
  const [year, month, day] = date.split("-");
  const weekday = new Intl.DateTimeFormat("zh-CN", { weekday: "long", timeZone: "Asia/Shanghai" }).format(new Date(`${date}T12:00:00+08:00`));
  return { year, month: Number(month), day, weekday };
}

/** Display one saved personal article with its original link. */
function Story({ entry, index }: { entry: DailyEntry; index: number }) {
  return (
    <article id={`story-${index}`} className="scroll-mt-24 border-b border-line py-6">
      <div className="flex items-baseline justify-between gap-4 text-[12px] text-ink-4">
        <span>{entry.source}</span>
        <span className="num shrink-0">{entry.score} 分</span>
      </div>
      <h3 className="mt-3 text-[19px] font-bold leading-[1.5] text-ink">{entry.title}</h3>
      <div className="mt-2 text-[15px] leading-[1.85] text-ink-2">
        <MarkdownContent content={entry.summary} />
      </div>
      <a href={entry.url} target="_blank" rel="noopener noreferrer" className="mt-4 inline-block text-[12px] font-medium text-accent hover:underline">阅读原文 ↗</a>
    </article>
  );
}

/** Render the user's saved daily reports as a newspaper-style issue. */
export default function UserDaily() {
  const { reports, report, csrfToken } = useLoaderData<typeof loader>();
  const navigate = useNavigate();
  const revalidator = useRevalidator();
  const entries = report?.entries ?? [];
  const lead = entries[0];
  const mark = report ? formatDate(report.date) : null;
  const sourceCount = new Set(entries.map((entry) => entry.source).filter(Boolean)).size;
  const [jobId, setJobId] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [activeStory, setActiveStory] = useState<number | null>(null);

  // 监听当前滚动位置，同步激活右侧篇目导航高亮
  useEffect(() => {
    if (!report || entries.length === 0) {
      return;
    }
    const count = Math.min(entries.length, 5);

    /** 计算当前阅读位置对应的篇目索引 */
    function onScroll() {
      // 头部偏移阈值：在尚未滑入头条正文时保持无高亮状态
      const offset = 140;
      const first = document.getElementById("story-0");
      if (first && first.getBoundingClientRect().top > offset) {
        setActiveStory(null);
        return;
      }

      let current = 0;
      for (let i = 0; i < count; i += 1) {
        const el = document.getElementById(`story-${i}`);
        if (el) {
          const rect = el.getBoundingClientRect();
          if (rect.top <= offset) {
            current = i;
          }
        }
      }
      setActiveStory(current);
    }

    onScroll();
    window.addEventListener("scroll", onScroll, { passive: true });

    return () => {
      window.removeEventListener("scroll", onScroll);
    };
  }, [report, entries.length]);

  // Follow the queued job until it completes, fails or the page is closed.
  useEffect(() => {
    if (jobId === null) {
      return;
    }
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    let attempts = 0;
    async function poll() {
      try {
        const response = await fetch(`/api/me/daily/generate/${jobId}`, { signal: controller.signal });
        const result = await response.json() as { status?: string; error?: string };
        if (!response.ok) {
          throw new Error(result.error || "无法读取日报生成状态");
        }
        if (result.status === "succeeded") {
          setJobId(null);
          setBusy(false);
          setMessage("日报已生成。");
          void navigate("/daily");
          revalidator.revalidate();
          return;
        }
        if (result.status === "empty" || result.status === "failed") {
          setJobId(null);
          setBusy(false);
          if (result.status === "empty") {
            setMessage("这段时间没有可整理的个人热点。");
          } else {
            setError("日报生成失败，请检查模型配置后重试。");
          }
          return;
        }
        attempts += 1;
        if (attempts >= 180) {
          throw new Error("日报仍在生成，请稍后刷新查看。");
        }
        timer = setTimeout(() => void poll(), 1000);
      } catch (cause) {
        if (!controller.signal.aborted) {
          setJobId(null);
          setBusy(false);
          setError(cause instanceof Error ? cause.message : "日报生成失败");
        }
      }
    }
    void poll();
    return () => { controller.abort(); clearTimeout(timer); };
  }, [jobId, navigate, revalidator]);

  /** Request an on-demand issue using the current user's session. */
  async function generate() {
    setBusy(true);
    setMessage("");
    setError("");
    try {
      const result = await postJSON<{ jobId: number }>("/api/me/daily/generate", {}, csrfToken);
      setJobId(result.jobId);
    } catch (cause) {
      setBusy(false);
      setError(cause instanceof Error ? cause.message : "无法提交日报任务");
    }
  }

  return (
    <div className="report-shell lg:-mx-7 lg:-mb-[72px] lg:-mt-6 lg:flex lg:min-h-dvh">
      <aside className="border-b border-line px-1 pb-5 lg:sticky lg:top-0 lg:h-dvh lg:w-[280px] lg:shrink-0 lg:overflow-y-auto lg:border-b-0 lg:border-r lg:px-5 lg:pt-8">
        <Link to="/" className="text-[12px] text-ink-4 hover:text-accent">← 我的热点</Link>
        <div className="mt-6 border-b border-line-strong pb-3 text-[12px] font-semibold tracking-[0.2em] text-ink">往期日报</div>
        <nav aria-label="往期日报" className="mt-2 flex gap-2 overflow-x-auto lg:block lg:space-y-1 lg:overflow-visible">
          {reports.map((item) => {
            const issue = formatDate(item.date);
            const current = item.id === report?.id;
            return (
              <Link key={item.id} to={`/daily?id=${encodeURIComponent(item.id)}`} aria-current={current ? "page" : undefined} className={`flex min-w-[190px] gap-3 rounded-tile px-3 py-3 transition-colors lg:min-w-0 ${current ? "bg-accent-soft" : "hover:bg-bg-sunk"}`}>
                <span className="flex w-9 shrink-0 flex-col text-center"><span className={`num text-[22px] font-black leading-none ${current ? "text-accent" : "text-ink"}`}>{issue.day}</span><span className="mt-1 text-[10px] text-ink-4">{issue.month}月</span></span>
                <span className="min-w-0"><span className="line-clamp-2 text-[13px] font-medium leading-5 text-ink-2">{item.title}</span>{item.id.startsWith("manual-") && <span className="mt-1 block text-[10px] text-ink-4">手动 · {new Date(item.createdAt).toLocaleTimeString("zh-CN", { timeZone: "Asia/Shanghai", hour: "2-digit", minute: "2-digit" })}</span>}</span>
              </Link>
            );
          })}
        </nav>
      </aside>

      <main className="min-w-0 flex-1 px-1 pb-16 pt-7 lg:px-10 lg:pt-9">
        <div className="mx-auto flex max-w-[1100px] flex-wrap items-center justify-end gap-3">
          {message && <p role="status" className="text-[12px] text-ink-3">{message}</p>}
          {error && <p role="alert" className="text-[12px] text-hot">{error}</p>}
          <button type="button" onClick={() => void generate()} disabled={busy} className="rounded-full bg-accent px-5 py-2.5 text-[13px] font-semibold text-accent-contrast hover:bg-accent-ink disabled:opacity-55">{busy ? "正在生成…" : "立刻生成"}</button>
        </div>
        {!report || !mark ? (
          <div className="mx-auto max-w-[1100px] py-12"><p className="text-[12px] tracking-[0.2em] text-accent">每日 08:00 · 北京时间</p><h1 className="mt-5 text-[40px] font-black text-ink">我的日报</h1><p className="mt-5 border-t border-line pt-6 text-[14px] leading-7 text-ink-3">有个人热点后，可以立刻生成日报，也可以等待下一期自动生成。</p></div>
        ) : (
          <div className="mx-auto max-w-[1100px]">
            <header>
              <div className="flex flex-wrap items-center justify-between gap-3 text-[12px] text-ink-4"><span>{mark.year} 年 {mark.month} 月 {Number(mark.day)} 日 · {mark.weekday}</span><span className="tracking-[0.2em]">个人 · 每日要闻</span><span>每天 08:00 出刊</span></div>
              <div className="flex items-center justify-between gap-5 py-9 sm:py-12">
                <div><h1 className="text-[48px] font-black leading-none tracking-[-0.05em] text-ink sm:text-[76px]"><span className="text-accent">我的</span>日报</h1><p className="mt-4 text-[11px] tracking-[0.24em] text-ink-4">你的每日信息摘要</p></div>
                <div className="well shrink-0 rounded-panel px-5 py-4 text-center sm:px-8"><Halftone seed={`personal-daily-${report.date}`} className="num block text-[44px] font-black leading-none text-ink sm:text-[64px]">{mark.day}</Halftone><p className="mt-2 text-[11px] text-ink-3">{mark.year} 年 {mark.month} 月</p><p className="text-[11px] text-ink-4">{mark.weekday}</p></div>
              </div>
              <div className="flex flex-wrap items-baseline gap-x-8 gap-y-2 border-y border-line-strong py-4 text-[12px] text-ink-4"><span><strong className="num mr-1.5 text-[24px] text-ink">{entries.length}</strong>篇文章</span><span><strong className="num mr-1.5 text-[24px] text-ink">{sourceCount}</strong>个来源</span><span className="sm:ml-auto">{report.id.startsWith("manual-") ? "汇总上期至手动生成时的个人结果" : "汇总此前 24 小时的个人结果"}</span></div>
            </header>

            <div className="grid items-start lg:grid-cols-[minmax(0,1fr)_290px]">
              <section className="min-w-0 py-10 lg:pr-12">
                <div id="story-0" className="scroll-mt-24">
                  <Kicker>{lead ? "头条" : "本期导读"}</Kicker>
                  <h2 className="mt-6 text-[32px] font-black leading-[1.25] tracking-[-0.03em] text-ink sm:text-[46px]">{lead?.title ?? report.title}</h2>
                  <div className="mt-6 text-[16px] leading-[2] text-ink-2">
                    <MarkdownContent content={lead?.summary ?? report.summary} />
                  </div>
                  {lead && <p className="mt-5 text-[12px] text-ink-4">{lead.source} · <a href={lead.url} target="_blank" rel="noopener noreferrer" className="text-accent hover:underline">阅读原文 ↗</a></p>}
                </div>
                {lead && <div className="mt-10 border-t border-line-strong pt-7"><Kicker>本期导读</Kicker><h2 className="mt-4 text-[20px] font-bold text-ink">{report.title}</h2><div className="mt-3 text-[15px] leading-[1.9] text-ink-2">
                    <MarkdownContent content={report.summary} />
                  </div></div>}
                {entries.length > 1 && <div className="mt-10 border-t border-line-strong pt-7"><Kicker>本期文章</Kicker><div className="mt-2">{entries.slice(1).map((entry, index) => <Story key={entry.articleId} entry={entry} index={index + 1} />)}</div></div>}
              </section>
              {entries.length > 0 && (
                <aside className="border-t border-line py-8 lg:sticky lg:top-0 lg:self-start lg:max-h-dvh lg:overflow-y-auto lg:border-l lg:border-t-0 lg:pl-8 lg:pt-10 scrollbar-thin">
                  <Kicker>今日看点</Kicker>
                  <ol className="mt-3">
                    {entries.slice(0, 5).map((entry, index) => {
                      const isActive = activeStory === index;
                      return (
                        <li key={entry.articleId} className="border-b border-line py-4">
                          <a
                            href={`#story-${index}`}
                            aria-current={isActive ? "location" : undefined}
                            onClick={(e) => {
                              if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.altKey || e.shiftKey) {
                                return;
                              }
                              e.preventDefault();
                              scrollToAnchor(`story-${index}`);
                            }}
                            className="group flex gap-4"
                          >
                            <span className="num text-[25px] font-black leading-none text-accent">
                              {index + 1}
                            </span>
                            <span>
                              <span className={`block text-[14px] font-bold leading-6 transition-colors ${isActive ? "text-accent" : "text-ink group-hover:text-accent"}`}>
                                {entry.title}
                              </span>
                              <span className="mt-1 block text-[12px] text-ink-4">{entry.source}</span>
                            </span>
                          </a>
                        </li>
                      );
                    })}
                  </ol>
                </aside>
              )}
            </div>
          </div>
        )}
      </main>
    </div>
  );
}
