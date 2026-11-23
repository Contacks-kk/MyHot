import type { MediaView } from "@aihot/contracts/site";
import type { Screen } from "../components/shell/screens";
import { useEffect, useState } from "react";
import { data, Link, useLoaderData, useNavigate, useNavigation } from "react-router";
import type { Route } from "./+types/user-hot";
import { MediaThumbs } from "../features/feed/parts";
import { userApiGet } from "../lib/user-api.server";
import { useLocalePreference } from "../lib/local-state";
import { getDict } from "../lib/i18n";
import { PersonalFunnel, type FunnelView } from "../features/feed/PersonalFunnel";
import { CalendarPopover } from "../components/ui/CalendarPopover";
import { SourceAvatar } from "../components/ui/SourceAvatar";
import {
  IconCalendar,
  IconChevronDown,
  IconChevronRight,
  IconClose,
  IconDoc,
  IconFlame,
  IconSparkles,
  IconGrid,
  IconHeart,
  IconHistory,
  IconPlug,
  IconRss,
} from "../components/icons";
import { ThemeSwitch } from "../components/shell/ThemeSwitch";

export const handle: Screen = { tab: "hot", name: "个人热点榜单" };

interface HotItem {
  articleId: string;
  score: number;
  title: string;
  originalTitle?: string;
  summary: string;
  url: string;
  source: string;
  sourceIconUrl?: string;
  createdAt: string;
  publishedAt?: string | null;
  media?: MediaView[];
}

/** Load only the selected personal hotspot window and today's filter activity. */
export async function loader({ request }: Route.LoaderArgs) {
  const params = new URL(request.url).searchParams;
  const range = params.get("range") ?? "48h";
  const date = params.get("date") ?? "";
  const page = Number(params.get("page") ?? "1");
  const today = new Intl.DateTimeFormat("en-CA", { timeZone: "Asia/Shanghai" }).format(new Date());
  if (!["today", "48h", "date"].includes(range) || !Number.isInteger(page) || page < 1 || page > 100000 || (range === "48h" && page !== 1)) {
    throw data({ message: "时间范围或页码无效" }, { status: 400 });
  }
  if (range === "date" && (!/^\d{4}-\d{2}-\d{2}$/.test(date) || Number.isNaN(Date.parse(date)) || new Date(date).toISOString().slice(0, 10) !== date || date > today)) {
    throw data({ message: "请选择有效的历史日期" }, { status: 400 });
  }
  const selection = new URLSearchParams({ range, page: String(page) });
  if (range === "date") {
    selection.set("date", date);
  }

  // Read existing results and settings; opening the page never triggers model processing.
  const [hot, funnel, model] = await Promise.all([
    userApiGet<{ items: HotItem[]; hasMore: boolean }>(request, `/api/me/hot?${selection}`),
    userApiGet<FunnelView>(request, "/api/me/feed/funnel").catch((error) => {
      if (request.signal.aborted) {
        throw error;
      }
      return null;
    }),
    userApiGet<{ configured: boolean }>(request, "/api/me/model"),
  ]);
  return {
    ...hot,
    funnel,
    range,
    date,
    page,
    today,
    configured: model.configured,
  };
}

export const meta: Route.MetaFunction = () => [{ title: "个人热点榜单 · MyHOT" }, { name: "robots", content: "noindex, nofollow" }];

export const headers: Route.HeadersFunction = () => ({ "Cache-Control": "private, no-store", "X-Robots-Tag": "noindex, nofollow" });

/** Extract the clean domain or hostname from an article URL. */
function hostOf(url: string): string {
  try {
    return new URL(url).hostname.replace(/^www\./, "");
  } catch {
    return url;
  }
}

/** Render the personal hotspot list with localized UI. */
export default function UserHot() {
  const { items, funnel, range, date, page, today, hasMore, configured } = useLoaderData<typeof loader>();
  const navigate = useNavigate();
  const navigation = useNavigation();
  const locale = useLocalePreference();
  const dict = getDict(locale);
  const isEn = locale === "en";

  const [calendarOpen, setCalendarOpen] = useState(false);
  const [drawerOpen, setDrawerOpen] = useState(false);

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

  // Optimistic navigation states: update immediately on user action for silky smooth feel
  const pendingParams = navigation.location ? new URLSearchParams(navigation.location.search) : null;
  const activeRange = (pendingParams ? pendingParams.get("range") : null) ?? range;
  const activeDate = (pendingParams && pendingParams.get("range") === "date" ? pendingParams.get("date") : null) ?? date;
  const isNavigating = navigation.state === "loading";

  const selectedDay = activeRange === "date" ? activeDate : today;
  const windowLabel = activeRange === "48h" ? dict.article.last48Hours : selectedDay;

  /** Handle date selection from the custom calendar popover. */
  function handleSelectDate(selectedDateStr: string) {
    setCalendarOpen(false);

    // Navigate smoothly to selected date view
    void navigate(`/hot?range=date&date=${selectedDateStr}`, {
      preventScrollReset: true,
    });
  }

  /** Preserve the selected calendar window when paging through the day's results. */
  function pageURL(nextPage: number): string {
    const params = new URLSearchParams({
      range: activeRange,
      page: String(nextPage),
    });
    if (activeRange === "date") {
      params.set("date", activeDate);
    }
    return `/hot?${params}`;
  }

  return (
    <section className="pb-10 pt-5 lg:pt-0" aria-busy={isNavigating}>
      {/* 页面顶栏：包含标题、日期切换器以及移动端抽屉导航按钮 */}
      <div className="flex flex-col gap-y-3">
        {/* 顶部指示行：移动端左侧显示功能导航折叠按钮，右侧显示时间范围；桌面端仅靠左显示时间范围 */}
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
            <IconChevronRight size={13} className="text-ink-4" />
          </button>

          <p className="mono text-[10px] uppercase tracking-[.16em] text-ink-4 transition-colors">
            {windowLabel}
          </p>
        </div>

        <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-2.5">
          <h1 className="editorial-title text-[22px] font-semibold tracking-tight text-ink sm:text-[28px] lg:text-[32px]">
            {dict.article.hotRankTitle}
          </h1>

          <div className="relative shrink-0">
            <nav
              aria-label={isEn ? "Hotspot time range" : "热点时间范围"}
              className="flex items-center gap-1 rounded-full border border-line bg-bg-sunk p-1 text-[11.5px] sm:text-[12px] shadow-xs"
            >
              {[
                { key: "today", label: isEn ? "Today" : "今日" },
                { key: "48h", label: isEn ? "48 hours" : "48小时" },
              ].map((option) => (
                <Link
                  key={option.key}
                  to={`/hot?range=${option.key}`}
                  prefetch="intent"
                  preventScrollReset
                  aria-current={activeRange === option.key ? "page" : undefined}
                  className={`rounded-full px-2.5 sm:px-3.5 py-1 sm:py-1.5 transition-all duration-150 ${
                    activeRange === option.key
                      ? "bg-surface font-semibold text-accent shadow-xs"
                      : "text-ink-3 hover:text-ink"
                  }`}
                >
                  {option.label}
                </Link>
              ))}

              <div
                className={`relative flex items-center rounded-full px-2.5 sm:px-3.5 py-1 sm:py-1.5 transition-all duration-150 cursor-pointer ${
                  activeRange === "date"
                    ? "bg-surface font-semibold text-accent shadow-xs"
                    : "text-ink-3 hover:text-ink"
                }`}
              >
                <button
                  type="button"
                  onClick={() => {
                    setCalendarOpen(!calendarOpen);
                  }}
                  aria-expanded={calendarOpen}
                  className="flex items-center gap-1 sm:gap-1.5 focus:outline-none"
                >
                  <IconCalendar size={13} className={activeRange === "date" ? "text-accent" : "text-ink-4"} />
                  <span>{isEn ? "Other dates" : "其他日期"}</span>
                  <IconChevronDown
                    size={12}
                    className={`transition-transform duration-200 ${
                      calendarOpen ? "rotate-180 text-accent" : "text-ink-4"
                    }`}
                  />
                </button>
                <input
                  type="date"
                  tabIndex={-1}
                  aria-hidden="true"
                  value={range === "date" ? date : ""}
                  onChange={(event) => {
                    if (event.target.value) {
                      handleSelectDate(event.target.value);
                    }
                  }}
                  className="sr-only"
                />
              </div>
            </nav>

            <CalendarPopover
              isOpen={calendarOpen}
              onClose={() => {
                setCalendarOpen(false);
              }}
              selectedDate={activeDate}
              today={today}
              locale={locale}
              onSelectDate={handleSelectDate}
            />
          </div>
        </div>
      </div>

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

          {/* 抽屉内部导航列表（模型配置、关注项、我的日报、我的信源、历史归档、关于MyHOT） */}
          <nav className="scrollbar-thin flex-1 overflow-y-auto px-3 py-4" aria-label="快捷导航抽屉">
            <div className="px-2 pb-1.5 text-[11px] font-semibold uppercase tracking-wider text-ink-4">
              {dict.nav.workspace}
            </div>
            <div className="flex flex-col gap-1">
              <Link
                to="/hot"
                onClick={() => setDrawerOpen(false)}
                className="flex h-10 items-center gap-2.5 rounded-control bg-bg-sunk px-2.5 text-[13.5px] font-semibold text-accent transition-colors"
              >
                <span className="flex w-5 justify-center text-accent">
                  <IconFlame size={17} />
                </span>
                <span>{dict.nav.hot}</span>
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
                to="/daily"
                onClick={() => {
                  setDrawerOpen(false);
                }}
                className="flex h-10 items-center gap-2.5 rounded-control px-2.5 text-[13.5px] font-medium text-ink-3 transition-colors hover:bg-bg-sunk hover:text-ink"
              >
                <span className="flex w-5 justify-center text-accent">
                  <IconDoc size={17} />
                </span>
                <span>{dict.nav.daily}</span>
              </Link>
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
              <Link
                to="/recommendations"
                onClick={() => setDrawerOpen(false)}
                className="flex h-10 items-center gap-2.5 rounded-control px-2.5 text-[13.5px] font-medium text-ink-3 transition-colors hover:bg-bg-sunk hover:text-ink"
              >
                <span className="flex w-5 justify-center text-accent">
                  <IconSparkles size={17} />
                </span>
                <span>{dict.nav.recommendations}</span>
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
                {isEn ? "Theme Appearance" : "外观主题"}
              </span>
              <ThemeSwitch className="w-[112px]" />
            </div>
          </div>
        </aside>
      </div>

      {/* Silky smooth subtle loading indicator line */}
      <div className="h-0.5 w-full overflow-hidden my-2">
        {isNavigating && (
          <div className="h-full w-full bg-accent animate-pulse rounded-full" />
        )}
      </div>

      <p className="mb-5 mt-1 text-[12px] text-ink-4 transition-colors">
        {activeRange === "48h" ? dict.article.hotRankSubtitle : (isEn ? `${selectedDay} · Full day in Beijing time · Ranked by your model score` : `${selectedDay} · 北京时间全天 · 按你的模型评分排序`)}
      </p>

      {!configured && (
        <div className="mb-6 rounded-card border border-accent/20 bg-accent-soft/45 p-4 text-[12.5px] leading-relaxed text-ink-2">
          <p>{isEn ? "Configure your model and follow a source to receive personalized hotspots. The source's top tags are followed automatically." : "配置模型并关注信源后，即可接收个人热点。系统会自动关注该信源最相关的标签。"}</p>
          <div className="mt-2 flex gap-3 font-medium text-accent">
            <Link to="/model">{isEn ? "Configure Model" : "配置模型"}</Link>
            <Link to="/sources">{isEn ? "Follow Sources" : "关注信源"}</Link>
          </div>
        </div>
      )}

      {funnel && <PersonalFunnel funnel={funnel} isEn={isEn} />}

      <div className={`editorial-list divide-y divide-line border-y border-line bg-surface px-4 sm:px-6 transition-opacity duration-200 ${isNavigating ? "opacity-50 pointer-events-none" : "opacity-100"}`}>
        {items.map((item, index) => {
          const displayTitle = isEn && item.originalTitle ? item.originalTitle : item.title;
          const sourceHost = hostOf(item.url);
          return (
            <article key={item.articleId} className="group relative flex gap-4 py-5">
              <span className="mono flex size-8 shrink-0 items-center justify-center rounded-control bg-accent-soft text-[12px] font-semibold text-accent">
                {String((page - 1) * 50 + index + 1).padStart(2, "0")}
              </span>
              <div className="min-w-0 flex-1">
                <div className="flex items-start justify-between gap-3">
                  <h2 className="editorial-title text-[17px] font-semibold text-ink">
                    <Link
                      to={`/my/articles/${encodeURIComponent(item.articleId)}`}
                      className="transition-colors group-hover:text-accent after:absolute after:inset-0 after:content-['']"
                    >
                      {displayTitle}
                    </Link>
                  </h2>
                  <span className="mono rounded-full bg-accent-soft px-2 py-1 text-[10px] text-accent">
                    {item.score}
                  </span>
                </div>
                <p className="mt-2 text-[12.5px] leading-relaxed text-ink-3">{item.summary}</p>
                {item.media && item.media.length > 0 && <MediaThumbs media={item.media} className="mt-3" />}
                {/* 底部元信息行：左下角信源 URL 与右下角时间戳高度齐平 */}
                <div className="mt-2.5 flex items-center justify-between gap-3 text-[11px] text-ink-4">
                  <span className="flex min-w-0 items-center gap-1.5" title={item.url}>
                    <SourceAvatar name={sourceHost} iconUrl={item.sourceIconUrl} size={16} />
                    <span className="truncate">{isEn ? "Source: " : "来源："}{sourceHost}</span>
                  </span>
                  <time className="shrink-0" dateTime={item.publishedAt ?? item.createdAt}>
                    {new Date(item.publishedAt ?? item.createdAt).toLocaleString(isEn ? "en-US" : "zh-CN", {
                      timeZone: "Asia/Shanghai",
                    })}
                  </time>
                </div>
              </div>
            </article>
          );
        })}
        {!items.length && (
          <p className="py-10 text-center text-[13px] text-ink-4">
            {isEn ? "No processed hotspots in this time range." : "这个时间范围内还没有已加工的个人热点。"}
          </p>
        )}
      </div>

      {(page > 1 || hasMore) && (
        <nav aria-label={isEn ? "Hotspot pages" : "热点分页"} className="mt-5 flex items-center justify-center gap-5 text-[13px] text-accent">
          {page > 1 && <Link to={pageURL(page - 1)} preventScrollReset>{isEn ? "Previous" : "上一页"}</Link>}
          <span className="text-ink-4">{isEn ? `Page ${page}` : `第 ${page} 页`}</span>
          {hasMore && <Link to={pageURL(page + 1)} preventScrollReset>{isEn ? "Next" : "下一页"}</Link>}
        </nav>
      )}
    </section>
  );
}
