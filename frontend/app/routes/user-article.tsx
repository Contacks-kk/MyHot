import { BackLink } from "../components/ui/BackLink";
import { useState, useEffect } from "react";
import type { MediaView } from "@aihot/contracts/site";
import { Link, useLoaderData } from "react-router";
import type { Route } from "./+types/user-article";
import type { Screen } from "../components/shell/screens";
import { ArticleLayout, RailSection } from "../components/ui/Page";
import { MediaGallery } from "../features/item/MediaGallery";
import { userApiGet, userApiMe } from "../lib/user-api.server";
import { useLocalePreference } from "../lib/local-state";
import { getDict } from "../lib/i18n";
import { translateText } from "../lib/translate";

export const handle: Screen = { home: "hot" };

interface PersonalArticle {
  articleId: string;
  title: string;
  originalTitle: string;
  summary: string;
  reason: string | null;
  score: number;
  source: string;
  sourceId?: string;
  isSubscribed?: boolean;
  author: string | null;
  url: string;
  publishedAt: string | null;
  createdAt: string;
  media?: MediaView[];
}

/** Load the authenticated user's saved article result and user session info. */
export async function loader({ request, params }: Route.LoaderArgs) {
  const [user, article] = await Promise.all([
    userApiMe(request),
    userApiGet<PersonalArticle>(
      request,
      `/api/me/articles/${encodeURIComponent(params.id)}`,
      { notFound: true }
    ),
  ]);

  return {
    article,
    csrfToken: user?.csrfToken ?? "",
  };
}

export function meta({ loaderData }: Route.MetaArgs) {
  return [{ title: loaderData?.article.title ?? "文章不存在" }, { name: "robots", content: "noindex, nofollow" }];
}

export const headers: Route.HeadersFunction = () => ({ "Cache-Control": "private, no-store", "X-Robots-Tag": "noindex, nofollow" });

/** Format a saved article time in the selected locale. */
function displayTime(value: string, locale: "zh" | "en") {
  return new Intl.DateTimeFormat(locale === "en" ? "en-US" : "zh-CN", {
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
  const { article, csrfToken } = useLoaderData<typeof loader>();
  const locale = useLocalePreference();
  const dict = getDict(locale);
  const isEn = locale === "en";

  const [translatedSummary, setTranslatedSummary] = useState<string | null>(null);
  const [translating, setTranslating] = useState(false);
  const [showTranslated, setShowTranslated] = useState(false);

  const [isSubscribed, setIsSubscribed] = useState<boolean>(Boolean(article.isSubscribed));
  const [subscribing, setSubscribing] = useState<boolean>(false);
  const [toastMessage, setToastMessage] = useState<string | null>(null);

  // Clear toast alert automatically
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

  /** Follow the origin source of this recommended article. */
  async function handleSubscribe() {
    if (!article.sourceId || isSubscribed || subscribing) {
      return;
    }

    setSubscribing(true);

    try {
      const res = await fetch("/api/me/sources/subscribe", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": csrfToken,
        },
        body: JSON.stringify({
          sourceId: article.sourceId,
          intervalMinutes: 60,
          reuseWindowMinutes: 10,
        }),
      });

      if (!res.ok) {
        const failure = (await res.json().catch(() => null)) as { error?: string } | null;
        throw new Error(failure?.error || (isEn ? "Failed to subscribe" : "关注信源失败"));
      }

      setIsSubscribed(true);
      setToastMessage(
        isEn
          ? `Subscribed to "${article.source}". Future articles will be tracked.`
          : `已成功关注信源「${article.source}」，后续将自动为你跟踪打分`
      );
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setToastMessage(msg);
    } finally {
      setSubscribing(false);
    }
  }

  // Auto-translate summary when user switches to English, or user can toggle manually
  useEffect(() => {
    if (isEn && !translatedSummary && !translating) {
      setTranslating(true);
      translateText(article.summary, "en")
        .then((res) => {
          setTranslatedSummary(res);
          setShowTranslated(true);
        })
        .finally(() => setTranslating(false));
    } else if (isEn) {
      setShowTranslated(true);
    } else {
      setShowTranslated(false);
    }
  }, [isEn, article.summary]);

  /** Toggle between translated text and original AI summary */
  const toggleTranslation = () => {
    if (!translatedSummary && !translating) {
      setTranslating(true);
      translateText(article.summary, isEn ? "zh" : "en")
        .then((res) => {
          setTranslatedSummary(res);
          setShowTranslated(true);
        })
        .finally(() => setTranslating(false));
      return;
    }
    setShowTranslated((prev) => !prev);
  };

  const time = article.publishedAt ?? article.createdAt;
  const timeLabel = article.publishedAt ? dict.article.publishTime : "整理时间";
  const mediaList = article.media ?? [];

  // In EN mode: display original English title if available
  const hasOriginalTitle = Boolean(article.originalTitle && article.originalTitle !== article.title);
  const primaryTitle = isEn && hasOriginalTitle ? article.originalTitle : article.title;
  const secondaryTitle = isEn && hasOriginalTitle ? article.title : article.originalTitle;

  const currentSummary = showTranslated && translatedSummary ? translatedSummary : article.summary;

  const sourceDetails = (
    <>
      <p className="text-[15px] font-semibold text-ink">{article.source}</p>
      {article.author && <p className="mt-1 text-[13px] text-ink-3">{article.author}</p>}
      <p className="mt-5 text-[12px] text-ink-4">{timeLabel}</p>
      <time dateTime={time} className="mt-1 block text-[12px] text-ink-2">
        {displayTime(time, locale)}
      </time>
    </>
  );

  const recommendation = (
    <>
      <span className="inline-flex rounded-full bg-accent-soft px-3 py-1 text-[12px] font-medium text-accent">
        {dict.article.score} · {article.score}
      </span>
      {article.reason && <p className="mt-4 whitespace-pre-line text-[14px] leading-7 text-ink-2">{article.reason}</p>}
    </>
  );

  return (
    <div className="pb-14 pt-5 lg:pt-0">
      <ArticleLayout
        left={
          <>
            <BackLink to="/hot" label={dict.action.backToHot} />
            <RailSection title={dict.article.source}>{sourceDetails}</RailSection>
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
              {dict.action.openOriginal}
            </a>
            <RailSection title={dict.article.recommendReason}>{recommendation}</RailSection>
          </>
        }
      >
        <div className="2xl:hidden"><BackLink to="/hot" label={dict.action.backToHot} /></div>
        <article className="pt-7 2xl:pt-0">
          {article.sourceId && !isSubscribed && (
            <div className="mb-6 flex flex-wrap items-center justify-between gap-3 rounded-2xl border border-accent/20 bg-accent-soft/30 p-4">
              <div className="flex items-center gap-2.5">
                <span className="text-[18px]">✨</span>
                <div>
                  <div className="text-[13px] font-semibold text-ink">
                    {isEn ? `Recommended from "${article.source}"` : `来自推荐信源「${article.source}」`}
                  </div>
                  <div className="text-[11.5px] text-ink-3">
                    {isEn
                      ? "Discovered through your tag preferences. Follow to automatically track upcoming articles."
                      : "根据你关注的标签推荐。关注该信源后，系统将自动为你跟踪后续资讯。"}
                  </div>
                </div>
              </div>
              <button
                type="button"
                onClick={handleSubscribe}
                disabled={subscribing}
                className="inline-flex items-center gap-1.5 rounded-full bg-accent px-4 py-1.5 text-[12px] font-semibold text-white shadow-sm transition hover:opacity-90 active:scale-95 disabled:opacity-50"
              >
                {subscribing ? (
                  <span className="inline-block h-3.5 w-3.5 animate-spin rounded-full border-2 border-white border-t-transparent" />
                ) : (
                  <span>+</span>
                )}
                <span>{subscribing ? (isEn ? "Following..." : "关注中...") : (isEn ? "Follow Source" : "关注此信源")}</span>
              </button>
            </div>
          )}
          {article.sourceId && isSubscribed && (
            <div className="mb-6 flex items-center justify-between rounded-2xl border border-moss/20 bg-moss-soft/20 px-4 py-2.5 text-[12px] text-moss">
              <span>✓ {isEn ? `You are following "${article.source}"` : `你已关注信源「${article.source}」`}</span>
            </div>
          )}
          <h1 className="break-words text-[30px] font-bold leading-[1.3] tracking-[-0.025em] text-ink sm:text-[40px] lg:text-[44px]">
            {primaryTitle}
          </h1>
          {secondaryTitle && secondaryTitle !== primaryTitle && (
            <p className="mt-4 break-words text-[14px] leading-7 text-ink-4">{secondaryTitle}</p>
          )}
          <div className="mt-6 border-y border-line py-4 text-[12px] leading-6 text-ink-4 2xl:hidden">
            <span className="font-medium text-ink-2">{article.source}</span>
            {article.author && <span> · {article.author}</span>}
            <span> · {timeLabel} {displayTime(time, locale)}</span>
          </div>

          <section className="mt-10">
            <div className="flex items-center justify-between gap-3">
              <h2 className="text-[13px] font-bold text-accent">
                {showTranslated ? (isEn ? "AI Brief (English)" : "AI 导读（译文）") : dict.article.aiBrief}
              </h2>
              <button
                type="button"
                onClick={toggleTranslation}
                disabled={translating}
                className="inline-flex items-center gap-1.5 rounded-full border border-line bg-surface px-3 py-1 text-[11.5px] font-medium text-ink-2 transition-colors hover:border-accent hover:text-accent disabled:opacity-50"
              >
                {translating
                  ? dict.action.translating
                  : showTranslated
                  ? dict.action.showOriginal
                  : dict.action.translate}
              </button>
            </div>
            <p className="mt-4 whitespace-pre-line break-words text-[17px] leading-[1.9] text-ink sm:text-[20px]">
              {currentSummary}
            </p>
          </section>

          {mediaList.length > 0 && (
            <MediaGallery
              media={mediaList}
              postUrl={article.url}
              header={(visibleCount) => (
                <section className="mt-10 border-t border-line pt-6">
                  <div className="flex flex-wrap items-baseline justify-between gap-2">
                    <h2 className="text-[13px] font-bold text-ink">
                      {dict.article.originalMedia}（{visibleCount}）
                    </h2>
                    <span className="text-[11.5px] text-ink-4">
                      {dict.article.mediaNotice}
                    </span>
                  </div>
                </section>
              )}
            />
          )}

          <p className="mt-8 text-[13px] text-ink-4">
            {dict.article.source}：{article.source}
          </p>
          <a
            href={article.url}
            target="_blank"
            rel="noopener noreferrer"
            className="mt-5 inline-flex min-h-11 items-center rounded-full border border-line-strong px-5 text-[13px] text-ink-2 hover:border-accent hover:text-accent lg:hidden"
          >
            {dict.action.openOriginal}
          </a>
          <section className="mt-10 border-t border-line pt-5 lg:hidden">
            <h2 className="mb-3 text-[12px] font-semibold text-ink-3">{dict.article.recommendReason}</h2>
            {recommendation}
          </section>
        </article>
      </ArticleLayout>
    </div>
  );
}
