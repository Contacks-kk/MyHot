import type { MediaView } from "@aihot/contracts/site";
import { Link, useLoaderData } from "react-router";
import type { Route } from "./+types/user-article";
import type { Screen } from "../components/shell/screens";
import { ArticleLayout, RailSection } from "../components/ui/Page";
import { MediaGallery } from "../features/item/MediaGallery";
import { userApiGet } from "../lib/user-api.server";

export const handle: Screen = { home: "hot" };

interface PersonalArticle {
  articleId: string;
  title: string;
  originalTitle: string;
  summary: string;
  reason: string | null;
  score: number;
  source: string;
  author: string | null;
  url: string;
  publishedAt: string | null;
  createdAt: string;
  media?: MediaView[];
}

/** Load the authenticated user's saved article result. */
export async function loader({ request, params }: Route.LoaderArgs) {
  const article = await userApiGet<PersonalArticle>(request, `/api/me/articles/${encodeURIComponent(params.id)}`, { notFound: true });
  return { article };
}

export function meta({ loaderData }: Route.MetaArgs) {
  return [{ title: loaderData?.article.title ?? "文章不存在" }, { name: "robots", content: "noindex, nofollow" }];
}

export const headers: Route.HeadersFunction = () => ({ "Cache-Control": "private, no-store", "X-Robots-Tag": "noindex, nofollow" });

/** Format a saved article time in Beijing time. */
function displayTime(value: string) {
  return new Intl.DateTimeFormat("zh-CN", {
    timeZone: "Asia/Shanghai",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  }).format(new Date(value));
}

/** Render the personal result in the article reading layout with summary decoupled from attached media. */
export default function UserArticle() {
  const { article } = useLoaderData<typeof loader>();
  const time = article.publishedAt ?? article.createdAt;
  const timeLabel = article.publishedAt ? "发布时间" : "整理时间";
  const mediaList = article.media ?? [];
  const sourceDetails = (
    <>
      <p className="text-[15px] font-semibold text-ink">{article.source}</p>
      {article.author && <p className="mt-1 text-[13px] text-ink-3">{article.author}</p>}
      <p className="mt-5 text-[12px] text-ink-4">{timeLabel}</p>
      <time dateTime={time} className="mt-1 block text-[12px] text-ink-2">{displayTime(time)}</time>
    </>
  );
  const recommendation = (
    <>
      <span className="inline-flex rounded-full bg-accent-soft px-3 py-1 text-[12px] font-medium text-accent">
        AI 评分 · {article.score}
      </span>
      {article.reason && <p className="mt-4 whitespace-pre-line text-[14px] leading-7 text-ink-2">{article.reason}</p>}
    </>
  );

  return (
    <div className="pb-14 pt-5 lg:pt-0">
      <ArticleLayout
        left={
          <>
            <Link to="/hot" className="inline-flex text-[13px] text-ink-3 transition-colors hover:text-accent">
              ← 返回热点榜
            </Link>
            <RailSection title="来源">{sourceDetails}</RailSection>
          </>
        }
        right={
          <>
            <a
              href={article.url}
              target="_blank"
              rel="noopener noreferrer"
              className="flex min-h-11 items-center justify-center rounded-full border border-line-strong px-5 text-[13px] text-ink-2 transition-colors hover:border-accent hover:text-accent"
            >
              打开原文 ↗
            </a>
            <RailSection title="推荐理由">{recommendation}</RailSection>
          </>
        }
      >
        <Link to="/hot" className="inline-flex text-[13px] text-ink-3 hover:text-accent 2xl:hidden">
          ← 返回热点榜
        </Link>
        <article className="pt-7 2xl:pt-0">
          <h1 className="break-words text-[30px] font-bold leading-[1.3] tracking-[-0.025em] text-ink sm:text-[40px] lg:text-[44px]">
            {article.title}
          </h1>
          {article.originalTitle && article.originalTitle !== article.title && (
            <p className="mt-4 break-words text-[14px] leading-7 text-ink-4">{article.originalTitle}</p>
          )}
          <div className="mt-6 border-y border-line py-4 text-[12px] leading-6 text-ink-4 2xl:hidden">
            <span className="font-medium text-ink-2">{article.source}</span>
            {article.author && <span> · {article.author}</span>}
            <span> · {timeLabel} {displayTime(time)}</span>
          </div>
          <section className="mt-10">
            <h2 className="text-[13px] font-bold text-accent">AI 导读</h2>
            <p className="mt-4 whitespace-pre-line break-words text-[17px] leading-[1.9] text-ink sm:text-[20px]">
              {article.summary}
            </p>
          </section>
          {mediaList.length > 0 && (
            <MediaGallery
              media={mediaList}
              postUrl={article.url}
              header={(visibleCount) => (
                <section className="mt-10 border-t border-line pt-6">
                  <div className="flex flex-wrap items-baseline justify-between gap-2">
                    <h2 className="text-[13px] font-bold text-ink">原文附图（{visibleCount}）</h2>
                    <span className="text-[11.5px] text-ink-4">按原文顺序列出，对应上下文请打开原文查看</span>
                  </div>
                </section>
              )}
            />
          )}
          <p className="mt-8 text-[13px] text-ink-4">来源：{article.source}</p>
          <a
            href={article.url}
            target="_blank"
            rel="noopener noreferrer"
            className="mt-5 inline-flex min-h-11 items-center rounded-full border border-line-strong px-5 text-[13px] text-ink-2 hover:border-accent hover:text-accent lg:hidden"
          >
            打开原文 ↗
          </a>
          <section className="mt-10 border-t border-line pt-5 lg:hidden">
            <h2 className="mb-3 text-[12px] font-semibold text-ink-3">推荐理由</h2>
            {recommendation}
          </section>
        </article>
      </ArticleLayout>
    </div>
  );
}
