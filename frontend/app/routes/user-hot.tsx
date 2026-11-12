import type { MediaView } from "@aihot/contracts/site";
import type { Screen } from "../components/shell/screens";
import { Link, useLoaderData } from "react-router";
import type { Route } from "./+types/user-hot";
import { MediaThumbs } from "../features/feed/parts";
import { userApiGet } from "../lib/user-api.server";

export const handle: Screen = { tab: "hot", name: "个人热点" };

interface HotItem {
  articleId: string;
  score: number;
  title: string;
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

/** Render the personal hotspot list. */
export default function UserHot() {
  const { items } = useLoaderData<typeof loader>();
  return (
    <section className="pb-10">
      <Link to="/" className="text-[12px] text-ink-4 hover:text-accent">← 我的热点</Link>
      <p className="mono mt-5 text-[10px] uppercase tracking-[.16em] text-ink-4">LAST 48 HOURS</p>
      <h1 className="editorial-title mt-1 text-[32px] font-semibold tracking-tight text-ink">个人热点榜</h1>
      <p className="mb-5 mt-2 text-[12px] text-ink-4">按你的模型评分排序，最多展示 10 条。</p>
      <div className="editorial-list divide-y divide-line border-y border-line bg-surface px-4 sm:px-6">
        {items.map((item, index) => (
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
                    {item.title}
                  </Link>
                </h2>
                <span className="mono rounded-full bg-accent-soft px-2 py-1 text-[10px] text-accent">{item.score}</span>
              </div>
              <p className="mt-2 text-[12.5px] leading-relaxed text-ink-3">{item.summary}</p>
              {item.media && item.media.length > 0 && <MediaThumbs media={item.media} className="mt-3" />}
              <p className="mt-2 text-[11px] text-ink-4">
                {item.source} · {new Date(item.createdAt).toLocaleString("zh-CN", { timeZone: "Asia/Shanghai" })}
              </p>
              <a
                href={item.url}
                target="_blank"
                rel="noopener noreferrer"
                className="relative z-10 mt-2 inline-block text-[11.5px] text-accent"
              >
                阅读原文 ↗
              </a>
            </div>
          </article>
        ))}
        {!items.length && <p className="py-10 text-center text-[13px] text-ink-4">过去 48 小时还没有匹配到的文章。</p>}
      </div>
    </section>
  );
}
