import { BackLink } from "../components/ui/BackLink";
import type { MediaView } from "@aihot/contracts/site";
import type { Screen } from "../components/shell/screens";
import { Link, useLoaderData } from "react-router";
import type { Route } from "./+types/user-hot";
import { MediaThumbs } from "../features/feed/parts";
import { userApiGet } from "../lib/user-api.server";
import { useLocalePreference } from "../lib/local-state";
import { getDict } from "../lib/i18n";

export const handle: Screen = { tab: "hot", name: "个人热点" };

interface HotItem {
  articleId: string;
  score: number;
  title: string;
  originalTitle?: string;
  summary: string;
  url: string;
  source: string;
  createdAt: string;
  media?: MediaView[];
}

/** Load personal hotspots for the authenticated user. */
export async function loader({ request }: Route.LoaderArgs) {
  return userApiGet<{ items: HotItem[] }>(request, "/api/me/hot");
}

export const meta: Route.MetaFunction = () => [{ title: "个人热点榜 · MyHOT" }, { name: "robots", content: "noindex, nofollow" }];

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
  const { items } = useLoaderData<typeof loader>();
  const locale = useLocalePreference();
  const dict = getDict(locale);
  const isEn = locale === "en";

  return (
    <section className="pb-10">
      <BackLink to="/" label={dict.action.backToFeed} />
      <p className="mono mt-5 text-[10px] uppercase tracking-[.16em] text-ink-4">
        {dict.article.last48Hours}
      </p>
      <h1 className="editorial-title mt-1 text-[32px] font-semibold tracking-tight text-ink">
        {dict.article.hotRankTitle}
      </h1>
      <p className="mb-5 mt-2 text-[12px] text-ink-4">
        {dict.article.hotRankSubtitle}
      </p>
      <div className="editorial-list divide-y divide-line border-y border-line bg-surface px-4 sm:px-6">
        {items.map((item, index) => {
          const displayTitle = isEn && item.originalTitle ? item.originalTitle : item.title;
          return (
            <article key={item.articleId} className="group relative flex gap-4 py-5">
              <span className="mono flex size-8 shrink-0 items-center justify-center rounded-control bg-accent-soft text-[12px] font-semibold text-accent">
                {String(index + 1).padStart(2, "0")}
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
                  <time className="shrink-0">
                    {new Date(item.createdAt).toLocaleString(isEn ? "en-US" : "zh-CN", {
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
            {dict.article.noHotspots}
          </p>
        )}
      </div>
    </section>
  );
}
