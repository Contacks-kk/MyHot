import { useState } from "react";
import { Link, useLoaderData } from "react-router";
import type { Route } from "./+types/home";
import type { Screen } from "../components/shell/screens";
import type { MediaView } from "@aihot/contracts/site";
import { SITE } from "@aihot/site";
import { IconArrowUpRight, IconBolt, IconClock, IconPlug } from "../components/icons";
import { MediaThumbs } from "../features/feed/parts";
import { userApiGet, userApiMe } from "../lib/user-api.server";
import { postJSON } from "../features/account/common";
import { SpiderLanding } from "../features/home/SpiderLanding";
import { useLocalePreference } from "../lib/local-state";
import { getDict } from "../lib/i18n";

export const handle: Screen = { tab: "featured", name: "我的热点" };

interface Card {
  articleId: string;
  score: number;
  title: string;
  summary: string;
  reason?: string | null;
  url: string;
  source: string;
  publishedAt?: string | null;
  createdAt: string;
  media?: MediaView[];
}

interface Report {
  date: string;
  model: string;
  title: string;
  summary: string;
  createdAt: string;
}

interface FunnelItem {
  articleId: string;
  title: string;
  source: string;
  url: string;
  publishedAt?: string | null;
  createdAt: string;
}

interface FunnelView {
  totalScanned: number;
  passedCount: number;
  filteredCount: number;
  filteredItems: FunnelItem[];
}

export async function loader({ request }: Route.LoaderArgs) {
  const user = await userApiMe(request);
  if (!user) {
    const response = await fetch(`${process.env.GO_API_BASE_URL || "http://127.0.0.1:3002"}/api/auth/registration-status`, {
      signal: request.signal,
    }).catch(() => null);
    const status = response?.ok ? ((await response.json()) as { enabled: boolean }) : { enabled: false };
    return {
      user: null,
      registrationEnabled: status.enabled,
      csrfToken: "",
      feed: [] as Card[],
      hot: [] as Card[],
      reports: [] as Report[],
      funnel: { totalScanned: 0, passedCount: 0, filteredCount: 0, filteredItems: [] } as FunnelView,
      modelConfigured: false,
      interestsConfigured: false,
    };
  }
  const [feed, hot, daily, model, interests, funnel] = await Promise.all([
    userApiGet<{ items: Card[] }>(request, "/api/me/feed"),
    userApiGet<{ items: Card[] }>(request, "/api/me/hot"),
    userApiGet<{ reports: Report[] }>(request, "/api/me/daily"),
    userApiGet<{ configured: boolean }>(request, "/api/me/model"),
    userApiGet<{ keywords: string[]; entityIds: string[] }>(request, "/api/me/interests"),
    userApiGet<FunnelView>(request, "/api/me/feed/funnel").catch(() => ({
      totalScanned: 0,
      passedCount: 0,
      filteredCount: 0,
      filteredItems: [] as FunnelItem[],
    })),
  ]);
  return {
    user,
    registrationEnabled: false,
    csrfToken: user.csrfToken,
    feed: feed.items,
    hot: hot.items,
    reports: daily.reports,
    funnel: funnel ?? { totalScanned: 0, passedCount: 0, filteredCount: 0, filteredItems: [] },
    modelConfigured: model.configured,
    interestsConfigured: interests.keywords.length + interests.entityIds.length > 0,
  };
}

export const meta: Route.MetaFunction = () => [{ title: `我的热点 · ${SITE.name}` }, { name: "robots", content: "noindex, nofollow" }];
export const headers: Route.HeadersFunction = () => ({ "Cache-Control": "private, no-store", "X-Robots-Tag": "noindex, nofollow" });

/** Render a list of personal article results. */
function ResultList({ items }: { items: Card[] }) {
  const locale = useLocalePreference();
  const dict = getDict(locale);
  const isEn = locale === "en";

  if (!items.length) {
    return (
      <div className="rounded-card border border-dashed border-line-strong px-5 py-8 text-center text-[13px] text-ink-4">
        {dict.article.noResultsYet}
      </div>
    );
  }

  return (
    <div className="editorial-list divide-y divide-line border-y border-line bg-surface px-4 sm:px-6">
      {items.map((item) => (
        <article key={item.articleId} className="group relative py-5">
          <div className="flex items-start gap-3">
            <span className="mono mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-full bg-accent-soft text-[11px] font-semibold text-accent">
              {item.score}
            </span>
            <div className="min-w-0 flex-1">
              <h2 className="editorial-title text-[17px] font-semibold leading-snug text-ink">
                <Link
                  to={`/my/articles/${encodeURIComponent(item.articleId)}`}
                  className="transition-colors group-hover:text-accent after:absolute after:inset-0 after:content-['']"
                >
                  {item.title}
                </Link>
              </h2>
              <p className="mt-2 whitespace-pre-line text-[13px] leading-relaxed text-ink-3">{item.summary}</p>
              {item.media && item.media.length > 0 && <MediaThumbs media={item.media} className="mt-3" />}
              <div className="mt-2 flex items-center gap-2 text-[11px] text-ink-4">
                <span>{item.source}</span>
                <span>·</span>
                <time>
                  {new Date(item.publishedAt ?? item.createdAt).toLocaleString(isEn ? "en-US" : "zh-CN", {
                    dateStyle: "short",
                    timeStyle: "short",
                    timeZone: "Asia/Shanghai",
                  })}
                </time>
              </div>
              {item.reason && (
                <p className="mt-2 text-[11px] text-ink-4">
                  {dict.article.matched}：{item.reason}
                </p>
              )}
              <a
                className="relative z-10 mt-2 inline-flex items-center gap-1 text-[11.5px] text-accent hover:underline"
                href={item.url}
                target="_blank"
                rel="noopener noreferrer"
              >
                {dict.action.readOriginal}
                <IconArrowUpRight size={13} />
              </a>
            </div>
          </div>
        </article>
      ))}
    </div>
  );
}

/** Render the personal workspace or introduction page. */
export default function Home() {
  const data = useLoaderData<typeof loader>();
  const [message, setMessage] = useState("");
  const [showFiltered, setShowFiltered] = useState(false);
  const locale = useLocalePreference();
  const dict = getDict(locale);
  const isEn = locale === "en";

  if (!data.user) {
    return <SpiderLanding registrationEnabled={data.registrationEnabled} />;
  }

  async function logout() {
    try {
      await postJSON("/api/auth/logout", {}, data.csrfToken);
      window.location.assign("/");
    } catch (e) {
      setMessage(e instanceof Error ? e.message : isEn ? "Logout failed" : "退出失败");
    }
  }

  return (
    <div className="pb-10">
      <header className="mb-7 flex flex-wrap items-start justify-between gap-4">
        <div>
          <p className="mono text-[10px] uppercase tracking-[.16em] text-ink-4">MY PERSONAL BRIEFING</p>
          <h1 className="editorial-title mt-1 text-[32px] font-semibold tracking-tight text-ink">
            {dict.nav.featured}
          </h1>
          <p className="mt-1 text-[12px] text-ink-4">{data.user.email}</p>
        </div>
        <button
          className="rounded-full border border-line-strong px-3.5 py-2 text-[12px] text-ink-3 hover:bg-surface"
          onClick={logout}
        >
          {dict.action.logout}
        </button>
      </header>

      <div className="mb-6 grid gap-3 sm:grid-cols-2">
        <Link to="/model" className="card flex items-center gap-3 p-4 transition-colors hover:border-accent/40">
          <span className="flex size-9 items-center justify-center rounded-full bg-accent-soft text-accent">
            <IconPlug size={17} />
          </span>
          <span>
            <span className="block text-[13px] font-semibold text-ink">{dict.nav.model}</span>
            <span className="mt-0.5 block text-[11px] text-ink-4">
              {data.modelConfigured
                ? isEn ? "Configured · Click to manage" : "已配置，点击管理"
                : isEn ? "Add your own OpenAI-compatible model" : "添加自己的 OpenAI 兼容模型"}
            </span>
          </span>
          <IconArrowUpRight size={15} className="ml-auto text-ink-4" />
        </Link>
        <Link to="/interests" className="card flex items-center gap-3 p-4 transition-colors hover:border-accent/40">
          <span className="flex size-9 items-center justify-center rounded-full bg-accent-soft text-accent">
            <IconBolt size={17} />
          </span>
          <span>
            <span className="block text-[13px] font-semibold text-ink">{dict.nav.interests}</span>
            <span className="mt-0.5 block text-[11px] text-ink-4">
              {data.interestsConfigured
                ? isEn ? "Configured · Affects future articles" : "已设置，调整只影响新文章"
                : isEn ? "Choose companies and keywords" : "选择公司实体和关键词"}
            </span>
          </span>
          <IconArrowUpRight size={15} className="ml-auto text-ink-4" />
        </Link>
      </div>

      {(!data.modelConfigured || !data.interestsConfigured) && (
        <div className="mb-7 rounded-card border border-accent/20 bg-accent-soft/45 p-4 text-[12.5px] leading-relaxed text-ink-2">
          {isEn
            ? "After configuring your model and interests, the system generates private summaries when new articles match. Historical articles are not backfilled."
            : "完成模型和关注项设置后，系统会在新文章命中时为你生成私人摘要。历史文章不会自动回填。"}
          <div className="mt-2 flex gap-3">
            <Link to="/model" className="font-medium text-accent">
              {isEn ? "Configure Model" : "配置模型"}
            </Link>
            <Link to="/interests" className="font-medium text-accent">
              {isEn ? "Set Interests" : "设置关注项"}
            </Link>
          </div>
        </div>
      )}

      {data.funnel && data.funnel.totalScanned > 0 && (
        <section className="mb-7 rounded-card border border-line bg-surface p-4 sm:p-5">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="flex items-center gap-2.5">
              <span className="flex size-7 shrink-0 items-center justify-center rounded-full bg-accent-soft text-accent">
                <IconBolt size={14} />
              </span>
              <div>
                <h2 className="text-[13.5px] font-semibold text-ink">
                  {isEn ? "Today's Stream & Filter Funnel" : "今日信息流与过滤动态"}
                </h2>
                <p className="mt-0.5 text-[12px] text-ink-3">
                  {isEn
                    ? `Scanned ${data.funnel.totalScanned} articles · ${data.funnel.passedCount} evaluated · ${data.funnel.filteredCount} filtered by labels (saved ~${data.funnel.filteredCount} model calls)`
                    : `已扫描 ${data.funnel.totalScanned} 篇资讯 · ${data.funnel.passedCount} 篇精读入库 · ${data.funnel.filteredCount} 篇通过标签自动过滤（为你节省约 ${data.funnel.filteredCount} 次模型调用）`}
                </p>
              </div>
            </div>
            <div className="flex items-center gap-3 text-[12px]">
              {data.funnel.filteredCount > 0 && (
                <button
                  type="button"
                  onClick={() => setShowFiltered(!showFiltered)}
                  className="font-medium text-accent hover:underline"
                >
                  {showFiltered
                    ? (isEn ? "Hide Filtered ▲" : "收起已拦截清单 ▲")
                    : (isEn ? `View ${data.funnel.filteredCount} Filtered ▼` : `查看 ${data.funnel.filteredCount} 篇已拦截清单 ▼`)}
                </button>
              )}
              <Link to="/interests" className="text-ink-4 hover:text-accent">
                {isEn ? "Interests" : "调整关注项"}
              </Link>
              <Link to="/sources" className="text-ink-4 hover:text-accent">
                {isEn ? "Sources" : "管理信源"}
              </Link>
            </div>
          </div>

          {showFiltered && data.funnel.filteredItems.length > 0 && (
            <div className="mt-4 border-t border-line pt-3">
              <p className="mb-2 text-[11px] text-ink-4">
                {isEn
                  ? "The following articles did not match your configured keywords/entities, so model evaluation was skipped:"
                  : "以下文章未命中你当前设置的关键词或公司实体，系统未调用模型打分："}
              </p>
              <ul className="divide-y divide-line-soft">
                {data.funnel.filteredItems.map((item) => (
                  <li key={item.articleId} className="flex items-start justify-between gap-4 py-2">
                    <div className="min-w-0 flex-1">
                      <a
                        href={item.url}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="line-clamp-1 text-[12.5px] font-medium text-ink hover:text-accent hover:underline"
                      >
                        {item.title} ↗
                      </a>
                      <div className="mt-0.5 text-[11px] text-ink-4">
                        <span>{item.source}</span>
                        <span> · </span>
                        <time>
                          {new Date(item.publishedAt ?? item.createdAt).toLocaleTimeString(isEn ? "en-US" : "zh-CN", {
                            timeZone: "Asia/Shanghai",
                            hour: "2-digit",
                            minute: "2-digit",
                          })}
                        </time>
                      </div>
                    </div>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </section>
      )}

      <section className="mb-8">
        <div className="mb-3 flex items-center justify-between">
          <h2 className="editorial-title text-[21px] font-semibold text-ink">
            {isEn ? "48-Hour Hotspots" : "48 小时个人热点"}
          </h2>
          <Link to="/hot" className="text-[12px] text-accent">
            {isEn ? "Open Hotlist" : "打开热点榜"}
          </Link>
        </div>
        <ResultList items={data.hot} />
      </section>

      <section className="mb-8">
        <div className="mb-3 flex items-center justify-between">
          <h2 className="editorial-title text-[21px] font-semibold text-ink">
            {isEn ? "Latest Signals" : "最新整理"}
          </h2>
          <Link to="/history" className="text-[12px] text-accent">
            {isEn ? "View Archive" : "查看旧站历史"}
          </Link>
        </div>
        <ResultList items={data.feed.slice(0, 10)} />
      </section>

      {data.reports[0] && (
        <section className="rounded-card border border-line bg-surface p-5">
          <div className="flex items-center gap-2 text-[12px] text-ink-4">
            <IconClock size={15} />
            {data.reports[0].date} {isEn ? "Daily Brief" : "每日简报"}
          </div>
          <h2 className="mt-2 text-[16px] font-semibold text-ink">{data.reports[0].title}</h2>
          <p className="mt-2 whitespace-pre-line text-[13px] leading-relaxed text-ink-3">{data.reports[0].summary}</p>
          <Link to="/daily" className="mt-3 inline-block text-[12px] font-medium text-accent">
            {isEn ? "View Briefing Archive" : "查看日报历史"}
          </Link>
        </section>
      )}

      {message && <p role="alert" className="mt-4 text-[12px] text-hot">{message}</p>}
    </div>
  );
}
