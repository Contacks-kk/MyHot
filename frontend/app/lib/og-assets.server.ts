import { CARDS, ITEM_COPY, REPORTS, SITE, subjectAfter, withSubject } from "@aihot/site";
import { CATEGORY_LABELS } from "@aihot/contracts/taxonomy";
import { beijingDate } from "@aihot/contracts/time";
import topicsDocument from "../../../shared/industry/topics.json" with { type: "json" };
import type { OgCard } from "@aihot/og-renderer/og";
import type { Poster } from "@aihot/og-renderer/poster";

type TopicGroup = { key: string; name: string };
type Topic = { slug: string; name: string; group: string; definition: string };
const { groups, topics } = topicsDocument as { groups: TopicGroup[]; topics: Topic[] };
const SITE_URL = (process.env.SITE_URL || SITE.defaultUrl).replace(/\/+$/, "");

export type WebOgTarget =
  | { kind: "poster"; id: string; apiPath: string }
  | { kind: "item"; id: string; apiPath: string }
  | { kind: "report"; reportKind: "daily" | "weekly" | "monthly"; key: string; apiPath: string }
  | { kind: "story"; publicId: string; apiPath: string };

const pageCards: Record<string, OgCard> = {
  ...CARDS,
  topics: {
    kicker: "主题",
    title: `${topics.length} ${subjectAfter("个长期追踪的", "方向")}`,
    subtitle: `${groups.map((group) => group.name).join("、")}。`,
  },
};

/** Website, fixed-page and topic cards are Web presentation resources and need no content query. */
export function isWebOgPath(pathname: string): boolean {
  return pathname === "/og/site.png" || /^\/og\/(?:pages|topics|items|stories|posters)\/[^/]+$/.test(pathname)
    || /^\/og\/reports\/[^/]+\/[^/]+$/.test(pathname);
}

/** Returns the same card data used by the former API renderer, or null for an unknown image. */
export function webOgCard(pathname: string): OgCard | null {
  if (pathname === "/og/site.png") return pageCards.site ?? null;
  const match = /^\/og\/(pages|topics)\/([^/]+)$/.exec(pathname);
  if (!match) return null;
  const file = decodeURIComponent(match[2]!);
  if (!file.endsWith(".png")) return null;
  const slug = file.slice(0, -4);
  if (match[1] === "pages") return pageCards[slug] ?? null;
  const topic = topics.find((item) => item.slug === slug);
  if (!topic) return null;
  return {
    kicker: `主题 · ${groups.find((group) => group.key === topic.group)?.name ?? ""}`,
    title: `${topic.name} 最新动态`,
    subtitle: topic.definition,
  };
}

export function webOgTarget(pathname: string): WebOgTarget | null {
  let match = /^\/og\/items\/([^/]+)$/.exec(pathname);
  if (match) {
    const file = decodeURIComponent(match[1]!);
    if (!file.endsWith(".png")) return null;
    const id = file.slice(0, -4);
    return { kind: "item", id, apiPath: `/api/site/items/${encodeURIComponent(id)}` };
  }
  match = /^\/og\/posters\/([^/]+)$/.exec(pathname);
  if (match) {
    const file = decodeURIComponent(match[1]!);
    if (!file.endsWith(".png")) return null;
    const id = file.slice(0, -4);
    return { kind: "poster", id, apiPath: `/api/site/items/${encodeURIComponent(id)}` };
  }
  match = /^\/og\/reports\/([^/]+)\/([^/]+)$/.exec(pathname);
  if (match) {
    const reportKind = decodeURIComponent(match[1]!);
    const file = decodeURIComponent(match[2]!);
    if (!file.endsWith(".png") || !["daily", "weekly", "monthly"].includes(reportKind)) return null;
    const key = file.slice(0, -4);
    return { kind: "report", reportKind: reportKind as "daily" | "weekly" | "monthly", key, apiPath: `/api/site/reports/${reportKind}/${encodeURIComponent(key)}` };
  }
  match = /^\/og\/stories\/([^/]+)$/.exec(pathname);
  if (match) {
    const file = decodeURIComponent(match[1]!);
    if (!file.endsWith(".png")) return null;
    const publicId = file.slice(0, -4);
    return { kind: "story", publicId, apiPath: `/api/site/stories/${encodeURIComponent(publicId)}` };
  }
  return null;
}

type ItemPayload = {
  id?: unknown;
  title?: unknown;
  summary?: unknown;
  timelineAt?: unknown;
  category?: unknown;
  score?: unknown;
  selected?: unknown;
  source?: { name?: unknown } | null;
};

type ReportPayload = {
  kind?: unknown;
  key?: unknown;
  title?: unknown;
  sections?: unknown;
  lead?: { title?: unknown; leadParagraph?: unknown } | null;
  overview?: unknown;
  readingMinutes?: unknown;
};

type StoryPayload = {
  publicId?: unknown;
  title?: unknown;
  whyHot?: { rank?: unknown } | null;
  latest?: unknown;
  digest?: unknown;
  sourceCount?: unknown;
  reportCount?: unknown;
};

type WebOgContentTarget = Exclude<WebOgTarget, { kind: "poster" }>;

function objectPayload(value: unknown): Record<string, unknown> | null {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : null;
}

function categoryLabel(value: unknown): string | null {
  return typeof value === "string" && Object.hasOwn(CATEGORY_LABELS, value)
    ? CATEGORY_LABELS[value as keyof typeof CATEGORY_LABELS]
    : null;
}

export function webOgPoster(id: string, value: unknown): Poster | null {
  const data = objectPayload(value) as ItemPayload | null;
  if (!data || data.id !== id || typeof data.title !== "string" || typeof data.timelineAt !== "string" || typeof data.source?.name !== "string") return null;
  const score = typeof data.score === "number" ? data.score : null;
  return {
    url: `${SITE_URL}/items/${encodeURIComponent(id)}`,
    kicker: categoryLabel(data.category) ?? withSubject("动态"),
    title: data.title,
    summary: typeof data.summary === "string" ? data.summary : null,
    source: data.source.name,
    date: beijingDate(data.timelineAt),
    score: data.selected && ITEM_COPY.showScore ? score : null,
  };
}

export function webOgContentCard(target: WebOgContentTarget, value: unknown): OgCard | null {
  const data = objectPayload(value);
  if (!data) return null;
  if (target.kind === "item") {
    const item = data as ItemPayload;
    if (item.id !== target.id || typeof item.title !== "string" || typeof item.timelineAt !== "string" || typeof item.source?.name !== "string") return null;
    const category = categoryLabel(item.category);
    const score = typeof item.score === "number" ? item.score : null;
    return {
      kicker: category ?? withSubject("动态"),
      title: item.title,
      subtitle: typeof item.summary === "string" ? item.summary : null,
      meta: `${item.source.name} · ${beijingDate(item.timelineAt)}`,
      badge: item.selected && score !== null && ITEM_COPY.showScore ? { value: String(Math.round(score)), label: "精选评分" } : null,
    };
  }
  if (target.kind === "report") {
    const report = data as ReportPayload;
    if (report.kind !== target.reportKind || report.key !== target.key || typeof report.title !== "string" || !Array.isArray(report.sections)) return null;
    const name = withSubject(target.reportKind === "daily" ? "日报" : target.reportKind === "weekly" ? "周报" : "月报");
    const itemCount = report.sections.reduce((count: number, value) => {
      const section = objectPayload(value);
      return count + (Array.isArray(section?.items) ? section.items.length : 0);
    }, 0);
    return {
      kicker: `${name} · ${report.key}`,
      title: typeof report.lead?.title === "string" ? report.lead.title : report.title,
      subtitle: typeof report.lead?.leadParagraph === "string" ? report.lead.leadParagraph : typeof report.overview === "string" ? report.overview : null,
      meta: `${itemCount} ${REPORTS.shareUnit} · 约 ${Number(report.readingMinutes) || 0} 分钟读完`,
    };
  }
  const story = data as StoryPayload;
  if (typeof story.publicId !== "string" || story.publicId !== target.publicId || typeof story.title !== "string") return null;
  const rank = typeof story.whyHot?.rank === "number" ? story.whyHot.rank : null;
  return {
    kicker: rank ? `热点第 ${rank} · 事件` : "事件",
    title: story.title,
    subtitle: typeof story.latest === "string" ? story.latest : typeof story.digest === "string" ? story.digest : null,
    meta: `${Number(story.sourceCount) || 0} 个来源 · ${Number(story.reportCount) || 0} 篇报道`,
    accent: rank ? "hot" : "teal",
  };
}
