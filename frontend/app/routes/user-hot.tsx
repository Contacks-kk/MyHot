import type { MediaView } from "@aihot/contracts/site";
import type { Screen } from "../components/shell/screens";
import { useState } from "react";
import { data, Link, useLoaderData, useNavigate, useNavigation } from "react-router";
import type { Route } from "./+types/user-hot";
import { MediaThumbs } from "../features/feed/parts";
import { userApiGet } from "../lib/user-api.server";
import { useLocalePreference } from "../lib/local-state";
import { getDict } from "../lib/i18n";
import { PersonalFunnel, type FunnelView } from "../features/feed/PersonalFunnel";
import { CalendarPopover } from "../components/ui/CalendarPopover";
import { IconCalendar, IconChevronDown } from "../components/icons";

export const handle: Screen = { tab: "hot", name: "个人热点榜单" };

interface HotItem {
  articleId: string;
  score: number;
  title: string;
  originalTitle?: string;
  summary: string;
  url: string;
  source: string;
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
  const [hot, funnel, model, interests] = await Promise.all([
    userApiGet<{ items: HotItem[]; hasMore: boolean }>(request, `/api/me/hot?${selection}`),
    userApiGet<FunnelView>(request, "/api/me/feed/funnel").catch((error) => {
      if (request.signal.aborted) {
        throw error;
      }
      return null;
    }),
    userApiGet<{ configured: boolean }>(request, "/api/me/model"),
    userApiGet<{ keywords: string[]; entityIds: string[] }>(request, "/api/me/interests"),
  ]);
  return {
    ...hot,
    funnel,
    range,
    date,
    page,
    today,
    configured: model.configured && interests.keywords.length + interests.entityIds.length > 0,
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
    const params = new URLSearchParams({ range: activeRange, page: String(nextPage) });
    if (activeRange === "date") {
      params.set("date", activeDate);
    }
    return `/hot?${params}`;
  }

  return (
    <section className="pb-10 pt-5 lg:pt-0" aria-busy={isNavigating}>
      <p className="mono text-[10px] uppercase tracking-[.16em] text-ink-4 transition-colors">
        {windowLabel}
      </p>
      <div className="mt-1 flex flex-wrap items-center justify-between gap-x-6 gap-y-3">
        <h1 className="editorial-title text-[28px] font-semibold tracking-tight text-ink sm:text-[32px]">
          {dict.article.hotRankTitle}
        </h1>
        <div className="relative">
          <nav aria-label={isEn ? "Hotspot time range" : "热点时间范围"} className="flex items-center gap-1 rounded-full border border-line bg-bg-sunk p-1 text-[12px] shadow-xs">
            {[{ key: "today", label: isEn ? "Today" : "今日" }, { key: "48h", label: isEn ? "48 hours" : "48小时" }].map((option) => (
              <Link
                key={option.key}
                to={`/hot?range=${option.key}`}
                prefetch="intent"
                preventScrollReset
                aria-current={activeRange === option.key ? "page" : undefined}
                className={`rounded-full px-3.5 py-1.5 transition-all duration-150 ${
                  activeRange === option.key
                    ? "bg-surface font-semibold text-accent shadow-xs"
                    : "text-ink-3 hover:text-ink"
                }`}
              >
                {option.label}
              </Link>
            ))}

            <div
              className={`relative flex items-center rounded-full px-3.5 py-1.5 transition-all duration-150 cursor-pointer ${
                activeRange === "date"
                  ? "bg-surface font-semibold text-accent shadow-xs"
                  : "text-ink-3 hover:text-ink"
              }`}
            >
              <button
                type="button"
                onClick={() => setCalendarOpen(!calendarOpen)}
                aria-expanded={calendarOpen}
                className="flex items-center gap-1.5 focus:outline-none"
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
            onClose={() => setCalendarOpen(false)}
            selectedDate={activeDate}
            today={today}
            locale={locale}
            onSelectDate={handleSelectDate}
          />
        </div>
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
          <p>{isEn ? "Configure your model and interests to process new matching articles. Historical articles are not backfilled." : "完成模型和关注项设置后，系统会在新文章命中时为你生成私人摘要。历史文章不会自动回填。"}</p>
          <div className="mt-2 flex gap-3 font-medium text-accent">
            <Link to="/model">{isEn ? "Configure Model" : "配置模型"}</Link>
            <Link to="/interests">{isEn ? "Set Interests" : "设置关注项"}</Link>
          </div>
        </div>
      )}

      {funnel && <PersonalFunnel funnel={funnel} isEn={isEn} />}

      <div className={`editorial-list divide-y divide-line border-y border-line bg-surface px-4 sm:px-6 transition-opacity duration-200 ${isNavigating ? "opacity-50 pointer-events-none" : "opacity-100"}`}>
        {items.map((item, index) => {
          const displayTitle = isEn && item.originalTitle ? item.originalTitle : item.title;
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
                  <span className="min-w-0 truncate" title={item.url}>
                    {isEn ? "Source: " : "来源："}{hostOf(item.url)}
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
