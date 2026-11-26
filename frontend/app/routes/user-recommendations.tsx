import type { Screen } from "../components/shell/screens";
import { useEffect, useState } from "react";
import { Link, useLoaderData, useNavigate, useNavigation } from "react-router";
import type { Route } from "./+types/user-recommendations";
import { userApiGet, userApiMe } from "../lib/user-api.server";
import { useLocalePreference } from "../lib/local-state";
import { getDict } from "../lib/i18n";
import {
  IconClose,
  IconDoc,
  IconFlame,
  IconGrid,
  IconHeart,
  IconHistory,
  IconPlug,
  IconRss,
  IconSparkles,
  IconTrendUp,
} from "../components/icons";
import { ThemeSwitch } from "../components/shell/ThemeSwitch";
import { SourceAvatar } from "../components/ui/SourceAvatar";

export const handle: Screen = { tab: "recommendations", name: "为你推荐" };

interface RecommendedItem {
  articleId: string;
  score: number | null;
  title: string;
  summary: string;
  url: string;
  sourceId: string;
  sourceName: string;
  sourceIconUrl?: string;
  sourceKind: string;
  isSubscribed: boolean;
  createdAt: string;
  publishedAt?: string | null;
  matchedTags: string[];
}

/** Load the signed-in user's single selection of recommendations. */
export async function loader({ request }: Route.LoaderArgs) {
  const [user, recs] = await Promise.all([
    userApiMe(request),
    userApiGet<{ items: RecommendedItem[] }>(request, "/api/me/recommended-articles"),
  ]);

  return {
    csrfToken: user?.csrfToken ?? "",
    items: recs.items ?? [],
  };
}

export const meta: Route.MetaFunction = () => [
  { title: "为你推荐 · MyHOT" },
  { name: "robots", content: "noindex, nofollow" },
];

export const headers: Route.HeadersFunction = () => ({
  "Cache-Control": "private, no-store",
  "X-Robots-Tag": "noindex, nofollow",
});

/** Extract the clean domain or hostname from an article URL. */
function hostOf(url: string): string {
  try {
    return new URL(url).hostname.replace(/^www\./, "");
  } catch {
    return url;
  }
}

/** Render the recommended articles page with 1-click source follow action. */
export default function UserRecommendations() {
  const { csrfToken, items: initialItems } = useLoaderData<typeof loader>();
  const navigation = useNavigation();
  const locale = useLocalePreference();
  const dict = getDict(locale);
  const isEn = locale === "en";

  const [items, setItems] = useState<RecommendedItem[]>(initialItems);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [subscribingSourceId, setSubscribingSourceId] = useState<string | null>(null);
  const [toastMessage, setToastMessage] = useState<string | null>(null);

  // Sync state when page changes via navigation
  useEffect(() => {
    setItems(initialItems);
  }, [initialItems]);

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

  // Clear toast after 4 seconds
  useEffect(() => {
    if (!toastMessage) {
      return;
    }

    const timer = setTimeout(() => {
      setToastMessage(null);
    }, 4000);

    return () => {
      clearTimeout(timer);
    };
  }, [toastMessage]);

  const isNavigating = navigation.state === "loading";

  /** Handle 1-click follow for an origin source from a recommended article card. */
  async function handleSubscribe(sourceId: string, sourceName: string) {
    if (subscribingSourceId) {
      return;
    }

    setSubscribingSourceId(sourceId);

    try {
      const res = await fetch("/api/me/sources/subscribe", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": csrfToken,
        },
        body: JSON.stringify({
          sourceId,
          intervalMinutes: 60,
          reuseWindowMinutes: 10,
        }),
      });

      if (!res.ok) {
        const failure = (await res.json().catch(() => null)) as { error?: string } | null;
        throw new Error(failure?.error || (isEn ? "Failed to subscribe" : "关注信源失败"));
      }

      // 实时将当前列表中该信源的所有文章标记为已关注
      setItems((prev) =>
        prev.map((item) =>
          item.sourceId === sourceId
            ? {
                ...item,
                isSubscribed: true,
              }
            : item
        )
      );

      setToastMessage(
        isEn
          ? `Subscribed to "${sourceName}". Articles will be monitored and processed.`
          : `已成功关注“${sourceName}”，系统将持续采集并为你加工该信源内容！`
      );
    } catch (err) {
      alert(err instanceof Error ? err.message : (isEn ? "Subscription failed" : "关注失败"));
    } finally {
      setSubscribingSourceId(null);
    }
  }

  return (
    <section className="pb-10 pt-5 lg:pt-0" aria-busy={isNavigating}>
      {/* Toast notification banner */}
      {toastMessage && (
        <div className="fixed bottom-6 right-6 z-50 flex items-center gap-2 rounded-card bg-accent-ink px-4 py-2.5 text-[13px] font-medium text-accent-contrast shadow-lg animate-in fade-in slide-in-from-bottom-2">
          <span>✓</span>
          <span>{toastMessage}</span>
          <button
            type="button"
            onClick={() => setToastMessage(null)}
            className="ml-2 text-accent-contrast/70 hover:text-accent-contrast"
          >
            ✕
          </button>
        </div>
      )}

      {/* 页面顶栏：包含标题、模式切换器以及移动端抽屉导航按钮 */}
      <div className="flex flex-col gap-y-3">
        {/* 顶部指示行：移动端左侧显示功能导航折叠按钮，右侧显示说明；桌面端仅显示说明 */}
        <div className="flex items-center justify-between lg:justify-start">
          <button
            type="button"
            onClick={() => {
              setDrawerOpen(true);
            }}
            aria-expanded={drawerOpen}
            aria-label={isEn ? "Open navigation drawer" : "打开功能导航抽屉"}
            className="inline-flex items-center gap-1.5 rounded-full border border-line-strong bg-surface px-3 py-1.5 text-[12.5px] font-medium text-ink shadow-xs transition-all hover:border-accent hover:text-accent active:scale-95 lg:hidden"
          >
            <IconGrid size={14} className="text-accent" />
            <span>{isEn ? "Menu" : "功能导航"}</span>
          </button>

          <p className="mono text-[10px] uppercase tracking-[.16em] text-ink-4 transition-colors">
            {isEn ? "Tag-based picks" : "标签精选"}
          </p>
        </div>

        <h1 className="editorial-title text-[22px] font-semibold tracking-tight text-ink sm:text-[28px] lg:text-[32px] flex items-center gap-2">
          <span>{isEn ? "Recommended for You" : "为你推荐"}</span>
          <span className="text-[18px]">✨</span>
        </h1>
      </div>

      {/* 移动端侧滑抽屉柜 */}
      <div
        className={`fixed inset-0 z-50 transition-opacity duration-300 lg:hidden ${
          drawerOpen ? "opacity-100 pointer-events-auto" : "opacity-0 pointer-events-none"
        }`}
        aria-hidden={!drawerOpen}
      >
        <div
          className="absolute inset-0 bg-black/40 backdrop-blur-[2px]"
          onClick={() => {
            setDrawerOpen(false);
          }}
        />

        <aside
          className={`absolute inset-y-0 left-0 flex w-[280px] max-w-[82vw] flex-col border-r border-line bg-surface shadow-2xl transition-transform duration-300 ease-out ${
            drawerOpen ? "translate-x-0" : "-translate-x-full"
          }`}
        >
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

          <nav aria-label={isEn ? "Sidebar navigation" : "侧边栏导航"} className="flex-1 overflow-y-auto p-3">
            <div className="px-2 pb-1.5 pt-1 text-[11px] font-semibold uppercase tracking-wider text-ink-4">
              {dict.nav.workspace}
            </div>
            <div className="flex flex-col gap-1">
              <Link
                to="/hot"
                onClick={() => setDrawerOpen(false)}
                className="flex h-10 items-center gap-2.5 rounded-control px-2.5 text-[13.5px] font-medium text-ink-3 transition-colors hover:bg-bg-sunk hover:text-ink"
              >
                <span className="flex w-5 justify-center text-accent">
                  <IconFlame size={17} />
                </span>
                <span>{dict.nav.hot}</span>
              </Link>
              <Link
                to="/recommendations"
                onClick={() => setDrawerOpen(false)}
                className="flex h-10 items-center gap-2.5 rounded-control bg-bg-sunk px-2.5 text-[13.5px] font-semibold text-accent transition-colors"
              >
                <span className="flex w-5 justify-center text-accent">
                  <IconSparkles size={17} />
                </span>
                <span>{isEn ? "Recommended" : "为你推荐"}</span>
              </Link>
              <Link
                to="/interests"
                onClick={() => setDrawerOpen(false)}
                className="flex h-10 items-center gap-2.5 rounded-control px-2.5 text-[13.5px] font-medium text-ink-3 transition-colors hover:bg-bg-sunk hover:text-ink"
              >
                <span className="flex w-5 justify-center text-accent">
                  <IconGrid size={17} />
                </span>
                <span>{dict.nav.interests}</span>
              </Link>
              <Link
                to="/daily"
                onClick={() => setDrawerOpen(false)}
                className="flex h-10 items-center gap-2.5 rounded-control px-2.5 text-[13.5px] font-medium text-ink-3 transition-colors hover:bg-bg-sunk hover:text-ink"
              >
                <span className="flex w-5 justify-center text-accent">
                  <IconDoc size={17} />
                </span>
                <span>{dict.nav.daily}</span>
              </Link>
              <Link
                to="/model"
                onClick={() => setDrawerOpen(false)}
                className="flex h-10 items-center gap-2.5 rounded-control px-2.5 text-[13.5px] font-medium text-ink-3 transition-colors hover:bg-bg-sunk hover:text-ink"
              >
                <span className="flex w-5 justify-center text-accent">
                  <IconPlug size={17} />
                </span>
                <span>{dict.nav.model}</span>
              </Link>
              <Link
                to="/sources"
                onClick={() => setDrawerOpen(false)}
                className="flex h-10 items-center gap-2.5 rounded-control px-2.5 text-[13.5px] font-medium text-ink-3 transition-colors hover:bg-bg-sunk hover:text-ink"
              >
                <span className="flex w-5 justify-center text-accent">
                  <IconRss size={17} />
                </span>
                <span>{dict.nav.sources}</span>
              </Link>
              <Link
                to="/markets"
                onClick={() => setDrawerOpen(false)}
                className="flex h-10 items-center gap-2.5 rounded-control px-2.5 text-[13.5px] font-medium text-ink-3 transition-colors hover:bg-bg-sunk hover:text-ink"
              >
                <span className="flex w-5 justify-center text-accent">
                  <IconTrendUp size={17} />
                </span>
                <span>{dict.nav.markets}</span>
              </Link>
            </div>

            <div className="mt-4 border-t border-line/60 px-2 pb-1.5 pt-3 text-[11px] font-semibold uppercase tracking-wider text-ink-4">
              {dict.nav.historyAndAbout}
            </div>
            <div className="flex flex-col gap-1">
              <Link
                to="/history"
                onClick={() => setDrawerOpen(false)}
                className="flex h-10 items-center gap-2.5 rounded-control px-2.5 text-[13.5px] font-medium text-ink-3 transition-colors hover:bg-bg-sunk hover:text-ink"
              >
                <span className="flex w-5 justify-center text-accent">
                  <IconHistory size={17} />
                </span>
                <span>{dict.nav.history}</span>
              </Link>
              <Link
                to="/about"
                onClick={() => setDrawerOpen(false)}
                className="flex h-10 items-center gap-2.5 rounded-control px-2.5 text-[13.5px] font-medium text-ink-3 transition-colors hover:bg-bg-sunk hover:text-ink"
              >
                <span className="flex w-5 justify-center text-accent">
                  <IconHeart size={17} />
                </span>
                <span>{dict.nav.about}</span>
              </Link>
            </div>
          </nav>

          <div className="border-t border-line bg-surface/60 px-4 py-3.5">
            <div className="flex items-center justify-between gap-3">
              <span className="text-[13px] font-medium text-ink-3">
                {isEn ? "Theme Appearance" : "外观主题"}
              </span>
              <ThemeSwitch className="w-[112px]" />
            </div>
          </div>
        </aside>
      </div>

      {/* Loading indicator bar */}
      <div className="h-0.5 w-full overflow-hidden my-2">
        {isNavigating && <div className="h-full w-full bg-accent animate-pulse rounded-full" />}
      </div>

      <p className="mb-5 mt-1 text-[12px] text-ink-4 transition-colors">
        {isEn
          ? "Up to six articles that match your tags and are new to your personal feed."
          : "按你关注的标签，精选最多 6 篇尚未收录到个人热点的文章。"}
      </p>

      {/* Article list */}
      <div
        className={`editorial-list divide-y divide-line border-y border-line bg-surface px-4 sm:px-6 transition-opacity duration-200 ${
          isNavigating ? "opacity-50 pointer-events-none" : "opacity-100"
        }`}
      >
        {items.map((item, index) => {
          const isScored = item.score != null;
          const isWebList = item.sourceKind === "web_list";

          return (
            <article key={item.articleId} className="group relative py-5">
              <div className="flex gap-4">
                {/* Ranking number */}
                <span className="mono flex size-8 shrink-0 items-center justify-center rounded-control bg-accent-soft text-[12px] font-semibold text-accent">
                  {String(index + 1).padStart(2, "0")}
                </span>

                <div className="min-w-0 flex-1">
                  {/* Title & Score */}
                  <div className="flex items-start justify-between gap-3">
                    <h2 className="editorial-title text-[17px] font-semibold text-ink">
                      {isScored ? (
                        <Link to={`/items/${encodeURIComponent(item.articleId)}`} className="transition-colors group-hover:text-accent">
                          {item.title}
                        </Link>
                      ) : (
                        <a href={item.url} target="_blank" rel="noreferrer" className="transition-colors group-hover:text-accent">
                          {item.title}
                        </a>
                      )}
                    </h2>
                    <span className="mono rounded-full bg-accent-soft px-2 py-1 text-[10px] text-accent shrink-0">
                      {isScored ? item.score : (isEn ? "Source excerpt" : "原文摘要")}
                    </span>
                  </div>

                  {/* Summary */}
                  <p className="mt-2 text-[12.5px] leading-relaxed text-ink-3 line-clamp-3 sm:line-clamp-none">
                    {item.summary}
                  </p>

                  {/* Matched Tags Chips */}
                  {item.matchedTags && item.matchedTags.length > 0 && (
                    <div className="mt-2.5 flex flex-wrap gap-1.5">
                      {item.matchedTags.map((tag) => (
                        <span
                          key={tag}
                          className="inline-flex items-center gap-1 rounded-full bg-accent/10 border border-accent/20 px-2 py-0.5 text-[10.5px] font-medium text-accent-ink"
                        >
                          <span className="text-[10px]">🏷️</span>
                          <span>{tag}</span>
                        </span>
                      ))}
                    </div>
                  )}

                  {/* Card bottom: Source follow action & timestamp */}
                  <div className="mt-3 flex flex-wrap items-center justify-between gap-2 border-t border-line/60 pt-2.5 text-[11px] text-ink-4">
                    <div className="flex min-w-0 max-w-full items-center gap-2">
                      <span className="flex min-w-0 max-w-[200px] items-center gap-1.5 font-medium text-ink-2" title={item.sourceName}>
                        <SourceAvatar name={hostOf(item.url)} iconUrl={item.sourceIconUrl} size={16} />
                        <span className="truncate">{isEn ? "Source: " : "来源："}{item.sourceName}</span>
                      </span>
                      <span
                        className={`rounded-full px-1.5 py-0.5 text-[9.5px] font-semibold ${
                          isWebList
                            ? "bg-blue-100 text-blue-700 dark:bg-blue-950 dark:text-blue-300"
                            : "bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300"
                        }`}
                      >
                        {isWebList ? "网页列表" : "RSS"}
                      </span>

                      {/* 1-Click follow button */}
                      {item.isSubscribed ? (
                        <span className="rounded-full bg-paper-2 border border-line px-2 py-0.5 text-[10.5px] text-ink-4 font-medium">
                          ✓ {isEn ? "Following" : "已关注"}
                        </span>
                      ) : (
                        <button
                          type="button"
                          disabled={subscribingSourceId === item.sourceId}
                          onClick={() => void handleSubscribe(item.sourceId, item.sourceName)}
                          className="inline-flex items-center gap-1 rounded-full bg-accent px-2.5 py-0.5 text-[11px] font-semibold text-accent-contrast shadow-xs hover:bg-accent-ink transition-colors disabled:opacity-50"
                        >
                          <span>+</span>
                          <span>{subscribingSourceId === item.sourceId ? (isEn ? "Following..." : "关注中…") : (isEn ? "Follow Source" : "关注信源")}</span>
                        </button>
                      )}
                    </div>

                    <div className="flex items-center gap-3">
                      <a
                        href={item.url}
                        target="_blank"
                        rel="noreferrer"
                        className="text-ink-4 hover:text-accent truncate max-w-[120px] sm:max-w-none"
                        title={item.url}
                      >
                        {hostOf(item.url)} ↗
                      </a>
                      <time dateTime={item.publishedAt ?? item.createdAt}>
                        {new Date(item.publishedAt ?? item.createdAt).toLocaleString(isEn ? "en-US" : "zh-CN", {
                          timeZone: "Asia/Shanghai",
                          month: "numeric",
                          day: "numeric",
                          hour: "2-digit",
                          minute: "2-digit",
                        })}
                      </time>
                    </div>
                  </div>
                </div>
              </div>
            </article>
          );
        })}

        {!items.length && (
          <div className="py-16 text-center text-ink-4">
            <p className="text-[14px] font-medium text-ink">
              {isEn ? "No eligible recommendations yet" : "暂无符合条件的推荐文章"}
            </p>
            <div className="mt-4 flex justify-center gap-3 text-[12.5px] font-medium text-accent">
              <Link to="/interests">{dict.nav.interests} →</Link>
              <Link to="/sources">{dict.nav.sources} →</Link>
            </div>
          </div>
        )}
      </div>

    </section>
  );
}
