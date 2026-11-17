import { useEffect, useState } from "react";
import { Link, useLoaderData } from "react-router";
import type { Route } from "./+types/home";
import type { Screen } from "../components/shell/screens";
import type { MediaView } from "@aihot/contracts/site";
import { SITE } from "@aihot/site";
import {
  IconBolt,
  IconChevronRight,
  IconClose,
  IconGrid,
  IconHeart,
  IconHistory,
  IconPlug,
  IconRss,
} from "../components/icons";
import { MediaThumbs } from "../features/feed/parts";
import { userApiGet, userApiMe } from "../lib/user-api.server";
import { SpiderLanding } from "../features/home/SpiderLanding";
import { ThemeSwitch } from "../components/shell/ThemeSwitch";
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

/** Load user feed, hotspot cards, and model configuration status. */
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
      funnel: { totalScanned: 0, passedCount: 0, filteredCount: 0, filteredItems: [] } as FunnelView,
      modelConfigured: false,
      interestsConfigured: false,
    };
  }

  // Fetch personal feed, 48h hotspots, filter funnel stats, and user settings concurrently
  const [feed, hot, model, interests, funnel] = await Promise.all([
    userApiGet<{ items: Card[] }>(request, "/api/me/feed"),
    userApiGet<{ items: Card[] }>(request, "/api/me/hot"),
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
    funnel: funnel ?? { totalScanned: 0, passedCount: 0, filteredCount: 0, filteredItems: [] },
    modelConfigured: model.configured,
    interestsConfigured: interests.keywords.length + interests.entityIds.length > 0,
  };
}

export const meta: Route.MetaFunction = () => [{ title: `我的热点 · ${SITE.name}` }, { name: "robots", content: "noindex, nofollow" }];
export const headers: Route.HeadersFunction = () => ({ "Cache-Control": "private, no-store", "X-Robots-Tag": "noindex, nofollow" });

/** Extract the clean domain or hostname from an article URL. */
function hostOf(url: string): string {
  try {
    return new URL(url).hostname.replace(/^www\./, "");
  } catch {
    return url;
  }
}

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
              {/* 底部元信息行：左下角信源 URL 与右下角时间戳高度齐平 */}
              <div className="mt-2.5 flex items-center justify-between gap-3 text-[11px] text-ink-4">
                <span className="min-w-0 truncate" title={item.url}>
                  {isEn ? "Source: " : "来源："}{hostOf(item.url)}
                </span>
                <time className="shrink-0">
                  {new Date(item.publishedAt ?? item.createdAt).toLocaleString(isEn ? "en-US" : "zh-CN", {
                    timeZone: "Asia/Shanghai",
                  })}
                </time>
              </div>
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
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [showFiltered, setShowFiltered] = useState(false);
  const locale = useLocalePreference();
  const dict = getDict(locale);
  const isEn = locale === "en";

  // Handle escape key to close navigation drawer
  useEffect(() => {
    if (!drawerOpen) {
      return;
    }

    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setDrawerOpen(false);
      }
    };

    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
    };
  }, [drawerOpen]);

  if (!data.user) {
    return <SpiderLanding registrationEnabled={data.registrationEnabled} />;
  }

  return (
    <div className="pb-10">
      <header className="mb-7 pt-5 sm:pt-6 lg:pt-0">
        {/* 桌面端头部 */}
        <div className="hidden lg:flex items-start justify-between gap-4">
          <div>
            <p className="mono text-[10px] uppercase tracking-[.16em] text-ink-4">
              MY PERSONAL BRIEFING
            </p>
            <h1 className="editorial-title mt-1 text-[32px] font-semibold tracking-tight text-ink">
              {dict.nav.featured}
            </h1>
            <p className="mt-1 text-[12px] text-ink-4">{data.user.email}</p>
          </div>
        </div>

        {/* 移动端头部（左侧为品牌标与抽屉柜按钮，右侧为“我的热点”标题与用户邮箱） */}
        <div className="lg:hidden">
          <div className="flex items-start justify-between gap-3">
            <div>
              <p className="mono text-[10px] uppercase tracking-[.16em] text-ink-4">
                MY PERSONAL BRIEFING
              </p>
              <div className="mt-2">
                <button
                  type="button"
                  onClick={() => {
                    setDrawerOpen(true);
                  }}
                  aria-expanded={drawerOpen}
                  aria-label={isEn ? "Open navigation drawer" : "打开功能导航抽屉"}
                  className="inline-flex items-center gap-1.5 rounded-full border border-line-strong bg-surface px-3 py-1.5 text-[12.5px] font-medium text-ink shadow-sm transition-all hover:border-accent hover:text-accent active:scale-95"
                >
                  <IconGrid size={14} className="text-accent" />
                  <span>{isEn ? "Menu" : "功能导航"}</span>
                  <IconChevronRight size={13} className="text-ink-4" />
                </button>
              </div>
            </div>

            <div className="text-right">
              <h1 className="editorial-title text-[24px] font-semibold tracking-tight text-ink">
                {dict.nav.featured}
              </h1>
              <p className="mt-1 text-[11px] text-ink-4">{data.user.email}</p>
            </div>
          </div>
        </div>
      </header>

      {/* 移动端侧滑抽屉柜（Drawer Cabinet：点击从左向右滑出） */}
      <div
        className={`fixed inset-0 z-50 transition-opacity duration-300 lg:hidden ${
          drawerOpen ? "opacity-100 pointer-events-auto" : "opacity-0 pointer-events-none"
        }`}
        aria-hidden={!drawerOpen}
      >
        {/* 背景遮罩 */}
        <div
          className="absolute inset-0 bg-black/40 backdrop-blur-[2px]"
          onClick={() => {
            setDrawerOpen(false);
          }}
        />

        {/* 抽屉面板（从左侧向右滑入） */}
        <aside
          className={`absolute inset-y-0 left-0 flex w-[280px] max-w-[82vw] flex-col border-r border-line bg-surface shadow-2xl transition-transform duration-300 ease-out ${
            drawerOpen ? "translate-x-0" : "-translate-x-full"
          }`}
        >
          {/* 抽屉顶栏 */}
          <div className="flex h-14 items-center justify-between border-b border-line px-4">
            <div className="flex items-center gap-2">
              <IconGrid size={16} className="text-accent" />
              <span className="text-[14px] font-semibold text-ink">{isEn ? "Menu" : "功能导航"}</span>
            </div>
            <button
              type="button"
              onClick={() => {
                setDrawerOpen(false);
              }}
              aria-label={isEn ? "Close menu" : "关闭菜单"}
              className="flex size-8 items-center justify-center rounded-full text-ink-3 transition-colors hover:bg-bg-sunk hover:text-ink"
            >
              <IconClose size={17} />
            </button>
          </div>

          {/* 抽屉内部导航列表（模型配置、关注项、我的信源、历史归档、关于MyHOT） */}
          <nav className="scrollbar-thin flex-1 overflow-y-auto px-3 py-4" aria-label="快捷导航抽屉">
            <div className="px-2 pb-1.5 text-[11px] font-semibold uppercase tracking-wider text-ink-4">
              {dict.nav.workspace}
            </div>
            <div className="flex flex-col gap-1">
              <Link
                to="/model"
                onClick={() => {
                  setDrawerOpen(false);
                }}
                className="flex h-10 items-center gap-2.5 rounded-control px-2.5 text-[13.5px] font-medium text-ink-3 transition-colors hover:bg-bg-sunk hover:text-ink"
              >
                <span className="flex w-5 justify-center text-accent">
                  <IconPlug size={17} />
                </span>
                <span>{dict.nav.model}</span>
              </Link>
              <Link
                to="/interests"
                onClick={() => {
                  setDrawerOpen(false);
                }}
                className="flex h-10 items-center gap-2.5 rounded-control px-2.5 text-[13.5px] font-medium text-ink-3 transition-colors hover:bg-bg-sunk hover:text-ink"
              >
                <span className="flex w-5 justify-center text-accent">
                  <IconGrid size={17} />
                </span>
                <span>{dict.nav.interests}</span>
              </Link>
              <Link
                to="/sources"
                onClick={() => {
                  setDrawerOpen(false);
                }}
                className="flex h-10 items-center gap-2.5 rounded-control px-2.5 text-[13.5px] font-medium text-ink-3 transition-colors hover:bg-bg-sunk hover:text-ink"
              >
                <span className="flex w-5 justify-center text-accent">
                  <IconRss size={17} />
                </span>
                <span>{dict.nav.sources}</span>
              </Link>
            </div>

            <div className="mt-4 border-t border-line/60 px-2 pb-1.5 pt-3 text-[11px] font-semibold uppercase tracking-wider text-ink-4">
              {dict.nav.historyAndAbout}
            </div>
            <div className="flex flex-col gap-1">
              <Link
                to="/history"
                onClick={() => {
                  setDrawerOpen(false);
                }}
                className="flex h-10 items-center gap-2.5 rounded-control px-2.5 text-[13.5px] font-medium text-ink-3 transition-colors hover:bg-bg-sunk hover:text-ink"
              >
                <span className="flex w-5 justify-center text-accent">
                  <IconHistory size={17} />
                </span>
                <span>{dict.nav.history}</span>
              </Link>
              <Link
                to="/about"
                onClick={() => {
                  setDrawerOpen(false);
                }}
                className="flex h-10 items-center gap-2.5 rounded-control px-2.5 text-[13.5px] font-medium text-ink-3 transition-colors hover:bg-bg-sunk hover:text-ink"
              >
                <span className="flex w-5 justify-center text-accent">
                  <IconHeart size={17} />
                </span>
                <span>{dict.nav.about}</span>
              </Link>
            </div>
          </nav>

          {/* 抽屉底栏：外观主题切换按钮 */}
          <div className="border-t border-line bg-surface/60 px-4 py-3.5">
            <div className="flex items-center justify-between gap-3">
              <span className="text-[13px] font-medium text-ink-3">
                {dict.shell.appearance}
              </span>
              <ThemeSwitch className="w-[112px]" />
            </div>
          </div>
        </aside>
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
    </div>
  );
}
