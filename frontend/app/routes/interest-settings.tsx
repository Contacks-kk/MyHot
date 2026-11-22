import { useEffect, useMemo, useRef, useState } from "react";
import { useLoaderData } from "react-router";
import { BackLink } from "../components/ui/BackLink";
import type { Screen } from "../components/shell/screens";
import type { Route } from "./+types/interest-settings";
import { IconSearch } from "../components/icons";
import { FormMessage, fieldClass } from "../features/account/common";
import { userApiGet, userApiMe } from "../lib/user-api.server";

export const handle: Screen = { tab: "interests", name: "关注项" };

interface Label {
  id: string;
  name: string;
  kind: "entity" | "topic";
  definition: string;
  aliases: string[];
  status: "active" | "dormant";
  count: number | null;
  userCount?: number;
  createdAt?: string;
  lastEvidenceAt?: string;
}

/** Load the user's saved preferences and subscription-scoped label directory. */
export async function loader({ request }: Route.LoaderArgs) {
  const [user, interests, catalog, subscriptions] = await Promise.all([
    userApiMe(request),
    userApiGet<{ keywords: string[]; entityIds: string[]; labelIds: string[] }>(request, "/api/me/interests"),
    userApiGet<{ labels: Label[] }>(request, "/api/me/labels"),
    userApiGet<{ sources: { id: number; name: string; enabled: boolean; filterMode: "label" | "all"; tags: string[] }[] }>(request, "/api/me/sources"),
  ]);
  return {
    csrfToken: user?.csrfToken ?? "",
    ...interests,
    labels: catalog.labels,
    automaticFollows: subscriptions.sources.filter(source => source.enabled && source.filterMode === "label"),
  };
}

export const meta: Route.MetaFunction = () => [{ title: "关注项 · MyHOT" }, { name: "robots", content: "noindex, nofollow" }];
export const headers: Route.HeadersFunction = () => ({ "Cache-Control": "private, no-store", "X-Robots-Tag": "noindex, nofollow" });

/** Calculate hotness score for sorting by hot. */
function getHotScore(label: Label, isFollowed: boolean): number {
  let score = (label.count ?? 0) * 10 + (label.userCount ?? 0) * 20;
  if (label.status === "active") {
    score += 100;
  }
  if (isFollowed) {
    score += 50;
  }
  if (label.kind === "entity") {
    score += 10;
  }
  return score;
}

/** Calculate timestamp for sorting by time. */
function getLabelTime(label: Label): number {
  if (label.lastEvidenceAt) {
    const time = new Date(label.lastEvidenceAt).getTime();
    if (!Number.isNaN(time)) {
      return time;
    }
  }
  if (label.createdAt) {
    const time = new Date(label.createdAt).getTime();
    if (!Number.isNaN(time)) {
      return time;
    }
  }
  return 0;
}

/** Sort labels according to chosen mode. */
function sortLabels(items: Label[], mode: "hot" | "time" | "articles", selectedSet: Set<string>): Label[] {
  const result = [...items];
  if (mode === "hot") {
    result.sort((a, b) => {
      const scoreDiff = getHotScore(b, selectedSet.has(b.id)) - getHotScore(a, selectedSet.has(a.id));
      if (scoreDiff !== 0) {
        return scoreDiff;
      }
      return a.name.localeCompare(b.name, "zh");
    });
  } else if (mode === "time") {
    result.sort((a, b) => {
      const timeDiff = getLabelTime(b) - getLabelTime(a);
      if (timeDiff !== 0) {
        return timeDiff;
      }
      const countDiff = (b.count ?? 0) - (a.count ?? 0);
      if (countDiff !== 0) {
        return countDiff;
      }
      return a.name.localeCompare(b.name, "zh");
    });
  } else {
    // articles
    result.sort((a, b) => {
      const countDiff = (b.count ?? 0) - (a.count ?? 0);
      if (countDiff !== 0) {
        return countDiff;
      }
      return a.name.localeCompare(b.name, "zh");
    });
  }
  return result;
}

/** Matches label against search keyword fuzzily. */
function matchesFuzzy(label: Label, term: string): boolean {
  if (!term) {
    return true;
  }
  const lower = term.toLowerCase().trim();
  if (label.name.toLowerCase().includes(lower)) {
    return true;
  }
  if (label.id.toLowerCase().includes(lower)) {
    return true;
  }
  if (label.definition && label.definition.toLowerCase().includes(lower)) {
    return true;
  }
  if (label.aliases && label.aliases.some((alias) => alias.toLowerCase().includes(lower))) {
    return true;
  }
  return false;
}

/** Unified tag row component arranged one per row with category badge, user count, and article count. */
function TagRow({
  label,
  isFollowed,
  onToggle,
}: {
  label: Label;
  isFollowed: boolean;
  onToggle: (id: string) => void;
}) {
  const isEntity = label.kind === "entity";

  return (
    <div
      className={`relative rounded-control border p-4 sm:p-5 transition-all ${
        isFollowed
          ? "border-accent/50 bg-accent-soft/20 shadow-xs"
          : "border-line bg-surface hover:border-line-strong"
      }`}
    >
      {/* Header: Name, Category Badge, Status, and Action Button */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-2.5 min-w-0">
          <span
            className={`rounded-full px-2 py-0.5 text-[10.5px] font-semibold shrink-0 ${
              isEntity
                ? "bg-blue-100 text-blue-700 dark:bg-blue-950 dark:text-blue-300"
                : "bg-purple-100 text-purple-700 dark:bg-purple-950 dark:text-purple-300"
            }`}
          >
            {isEntity ? "公司与产品" : "技术主题"}
          </span>
          <strong className="text-[15.5px] font-semibold text-ink leading-snug">
            {label.name}
          </strong>
          {label.status === "dormant" && (
            <span className="rounded-full bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300 px-2 py-0.5 text-[10px] font-semibold">
              已休眠
            </span>
          )}
        </div>

        {/* Follow / Unfollow Action Button */}
        <div className="shrink-0 flex items-center">
          <button
            type="button"
            onClick={() => onToggle(label.id)}
            className={`rounded-full px-3.5 py-1.5 text-[12px] font-semibold transition-all ${
              isFollowed
                ? "bg-accent text-accent-contrast shadow-xs hover:bg-rose-600 hover:text-white"
                : "border border-line bg-surface text-ink-2 hover:border-accent hover:text-accent"
            }`}
          >
            {isFollowed ? "✓ 已关注" : "＋ 关注"}
          </button>
          <input
            type="checkbox"
            className="sr-only"
            checked={isFollowed}
            onChange={() => onToggle(label.id)}
            aria-label={`关注 ${label.name}`}
          />
        </div>
      </div>

      {/* Definition Description */}
      {label.definition && (
        <p className="mt-2.5 text-[12.5px] leading-relaxed text-ink-3">
          {label.definition}
        </p>
      )}

      {/* Aliases */}
      {label.aliases && label.aliases.length > 0 && (
        <div className="mt-2 flex flex-wrap items-center gap-1.5 text-[11px] text-ink-4">
          <span className="text-[10.5px]">别名:</span>
          {label.aliases.map((alias) => (
            <span
              key={alias}
              className="rounded bg-surface-2 px-1.5 py-0.2 text-[10.5px] text-ink-3"
            >
              {alias}
            </span>
          ))}
        </div>
      )}

      {/* Metrics Footer: Following Users & Related Articles */}
      <div className="mt-3.5 flex flex-wrap items-center justify-between gap-2 border-t border-line/60 pt-2.5 text-[11.5px] text-ink-4">
        <div className="flex items-center gap-2">
          <span>
            <strong className="font-semibold text-ink-2">{label.userCount ?? 0}</strong> 位用户关注
          </span>
          <span className="text-line-strong">·</span>
          <span>
            <strong className="font-semibold text-ink-2">{label.count ?? 0}</strong> 篇关联文章
          </span>
        </div>
        {label.lastEvidenceAt && (
          <span className="text-[10.5px] text-ink-4">
            最近命中: {new Date(label.lastEvidenceAt).toLocaleDateString()}
          </span>
        )}
      </div>
    </div>
  );
}

/** Render search list with fuzzy search, hot/time/article sorting, and two horizontal tabs. */
export default function InterestSettings() {
  const initial = useLoaderData<typeof loader>();
  const initialSelected = useMemo(
    () => new Set([...initial.entityIds, ...initial.labelIds]),
    [initial.entityIds, initial.labelIds],
  );

  const [selected, setSelected] = useState(() => new Set(initialSelected));
  const [activeTab, setActiveTab] = useState<"all" | "following">("all");
  const [followedTagFilter, setFollowedTagFilter] = useState<string>("all");
  const [search, setSearch] = useState("");
  const [sortBy, setSortBy] = useState<"hot" | "time" | "articles">("hot");
  const [error, setError] = useState("");

  const savingRef = useRef(false);
  const pendingSaveRef = useRef<Set<string> | null>(null);
  const selectedRef = useRef(selected);
  selectedRef.current = selected;

  const entityIDs = useMemo(
    () => new Set(initial.labels.filter(label => label.kind === "entity").map(label => label.id)),
    [initial.labels],
  );

  // Labels followed by user
  const followedLabels = useMemo(() => {
    return initial.labels.filter(label => selected.has(label.id));
  }, [initial.labels, selected]);

  // Reset tag filter if currently filtered tag was unfollowed
  useEffect(() => {
    if (followedTagFilter !== "all" && !selected.has(followedTagFilter)) {
      setFollowedTagFilter("all");
    }
  }, [selected, followedTagFilter]);

  /** Synchronize updated interest tags to backend API. */
  async function syncInterests(currentSelected: Set<string>) {
    if (savingRef.current) {
      pendingSaveRef.current = currentSelected;
      return;
    }

    savingRef.current = true;
    setError("");

    // Submit updated interests to Go backend
    try {
      const entityIds = [...currentSelected].filter((id) => {
        return entityIDs.has(id) || initial.entityIds.includes(id);
      });
      const labelIds = [...currentSelected].filter((id) => {
        return !entityIDs.has(id) && !initial.entityIds.includes(id);
      });

      const response = await fetch("/api/me/interests", {
        method: "PUT",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": initial.csrfToken,
        },
        body: JSON.stringify({
          keywords: initial.keywords,
          entityIds,
          labelIds,
        }),
      });

      const result = (await response.json().catch(() => ({}))) as { error?: string };
      if (!response.ok) {
        throw new Error(result.error || "关注状态同步失败");
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "关注状态同步失败");
    } finally {
      savingRef.current = false;
      if (pendingSaveRef.current) {
        const next = pendingSaveRef.current;
        pendingSaveRef.current = null;
        void syncInterests(next);
      }
    }
  }

  /** Toggle follow status for a single tag and immediately persist. */
  function toggle(id: string) {
    const next = new Set(selectedRef.current);
    if (next.has(id)) {
      next.delete(id);
    } else {
      next.add(id);
    }
    selectedRef.current = next;
    setSelected(next);

    // Persist immediately to backend
    void syncInterests(next);
  }

  // Filter and sort items based on active tab and toolbar criteria
  const currentTabItems = useMemo(() => {
    let sourceList: Label[];
    if (activeTab === "following") {
      sourceList = followedLabels;
      if (followedTagFilter !== "all") {
        sourceList = sourceList.filter(l => l.id === followedTagFilter);
      }
    } else {
      sourceList = initial.labels;
    }

    // Apply fuzzy search
    const filtered = sourceList.filter(label => matchesFuzzy(label, search));

    // Apply sort
    return sortLabels(filtered, sortBy, selected);
  }, [activeTab, followedLabels, followedTagFilter, initial.labels, search, sortBy, selected]);

  return (
    <section className="mx-auto max-w-[860px] pb-12">
      <BackLink to="/hot" label="个人热点榜单" />

      {/* Header section */}
      <div className="mt-3 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-[25px] font-semibold tracking-tight text-ink">关注项</h1>
          <p className="mt-1 text-[13px] text-ink-3">
            关注信源后会自动关注它最相关的 3 个稳定标签。你也可以手动补充关注。
          </p>
        </div>
        <div className="text-[12px] text-ink-4">
          手动关注 <strong className="text-accent">{selected.size}</strong> 个标签
        </div>
      </div>

      {initial.automaticFollows.length > 0 && (
        <section className="card mt-5 p-4" aria-label="信源自动关注">
          <h2 className="text-[14px] font-semibold text-ink">信源自动关注</h2>
          <p className="mt-1 text-[12px] text-ink-4">自动标签按信源分别生效，随近期文章更新。</p>
          <ul className="mt-2 divide-y divide-line">
            {initial.automaticFollows.map(source => (
              <li key={source.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2 text-[12px]">
                <span className="font-medium text-ink">{source.name}</span>
                <span className="text-ink-3">{source.tags.length > 0 ? source.tags.join(" · ") : "待识别，文章可先进入个人筛选"}</span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {/* Horizontal Tabs: Reserved only [全部标签] and [我关注的标签] */}
      <div className="mt-6 flex border-b border-line gap-2 overflow-x-auto scrollbar-none">
        <button
          type="button"
          onClick={() => {
            setActiveTab("all");
            setFollowedTagFilter("all");
          }}
          className={`pb-3 px-3.5 text-[13px] font-semibold border-b-2 transition-colors flex items-center gap-1.5 whitespace-nowrap ${
            activeTab === "all"
              ? "border-accent text-accent"
              : "border-transparent text-ink-3 hover:text-ink"
          }`}
        >
          <span>全部标签</span>
          <span className="rounded-full bg-surface-2 px-1.5 py-0.2 text-[10.5px] text-ink-3">
            {initial.labels.length}
          </span>
        </button>

        <button
          type="button"
          onClick={() => {
            setActiveTab("following");
            setFollowedTagFilter("all");
          }}
          className={`pb-3 px-3.5 text-[13px] font-semibold border-b-2 transition-colors flex items-center gap-1.5 whitespace-nowrap ${
            activeTab === "following"
              ? "border-accent text-accent"
              : "border-transparent text-ink-3 hover:text-ink"
          }`}
        >
          <span>我关注的标签</span>
          <span className="rounded-full bg-accent/15 px-2 py-0.5 text-[10.5px] font-bold text-accent">
            {selected.size}
          </span>
        </button>
      </div>

      <div className="mt-5 space-y-5">
        {/* Messages */}
        {error && <FormMessage error>{error}</FormMessage>}

        {/* In 'Following' tab: horizontal tag switching bar when user has followed tags */}
        {activeTab === "following" && followedLabels.length > 0 && (
          <div className="card p-3 sm:p-4">
            <div className="flex items-center justify-between pb-2 text-[12px] text-ink-4">
              <span>点击已关注标签可横向切换单项查看：</span>
              <span>共 {followedLabels.length} 个关注</span>
            </div>
            <div className="flex items-center gap-1.5 overflow-x-auto scrollbar-none pt-1 pb-1">
              <button
                type="button"
                onClick={() => setFollowedTagFilter("all")}
                className={`rounded-full px-3 py-1 text-[11.5px] font-semibold transition-colors shrink-0 ${
                  followedTagFilter === "all"
                    ? "bg-accent text-accent-contrast shadow-xs"
                    : "border border-line bg-surface text-ink-3 hover:text-ink"
                }`}
              >
                全部展示 ({followedLabels.length})
              </button>
              {followedLabels.map((lbl) => (
                <button
                  key={lbl.id}
                  type="button"
                  onClick={() => setFollowedTagFilter(lbl.id === followedTagFilter ? "all" : lbl.id)}
                  className={`rounded-full px-3 py-1 text-[11.5px] font-medium transition-colors shrink-0 flex items-center gap-1.5 ${
                    followedTagFilter === lbl.id
                      ? "bg-accent text-accent-contrast font-semibold shadow-xs"
                      : "border border-line bg-surface text-ink-2 hover:border-accent hover:text-accent"
                  }`}
                >
                  <span>{lbl.name}</span>
                  {lbl.count != null && (
                    <span className="opacity-80 text-[10.5px]">· {lbl.count}</span>
                  )}
                </button>
              ))}
            </div>
          </div>
        )}

        {/* Search & Sort Toolbar (without emojis) */}
        <div className="card p-4 sm:p-5 space-y-3.5">
          {/* Fuzzy Search Input */}
          <div className="relative flex items-center">
            <IconSearch size={15} className="absolute left-3.5 text-ink-4" />
            <input
              id="label-search"
              type="search"
              className={`${fieldClass} pl-9 pr-9 h-11 text-[13px]`}
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="模糊搜索标签名称、别名或技术描述（如：OpenAI、Agent、Cursor、DeepSeek...）"
            />
            {search && (
              <button
                type="button"
                onClick={() => setSearch("")}
                className="absolute right-3 text-ink-4 hover:text-ink text-[13px] px-1"
                title="清空搜索"
              >
                ✕
              </button>
            )}
          </div>

          {/* Sort Controls (without emojis) */}
          <div className="flex flex-wrap items-center justify-between gap-3 pt-1">
            <div className="flex items-center gap-2 text-[12px]">
              <span className="text-ink-4 font-medium">排序:</span>
              <div className="flex rounded-control border border-line bg-bg-sunk/30 p-0.5">
                <button
                  type="button"
                  onClick={() => setSortBy("hot")}
                  className={`rounded-[5px] px-2.5 py-1 text-[11.5px] font-medium transition-colors flex items-center gap-1 ${
                    sortBy === "hot"
                      ? "bg-surface text-accent font-semibold shadow-xs"
                      : "text-ink-3 hover:text-ink"
                  }`}
                >
                  <span>按热度</span>
                </button>
                <button
                  type="button"
                  onClick={() => setSortBy("time")}
                  className={`rounded-[5px] px-2.5 py-1 text-[11.5px] font-medium transition-colors flex items-center gap-1 ${
                    sortBy === "time"
                      ? "bg-surface text-accent font-semibold shadow-xs"
                      : "text-ink-3 hover:text-ink"
                  }`}
                >
                  <span>按时间</span>
                </button>
                <button
                  type="button"
                  onClick={() => setSortBy("articles")}
                  className={`rounded-[5px] px-2.5 py-1 text-[11.5px] font-medium transition-colors flex items-center gap-1 ${
                    sortBy === "articles"
                      ? "bg-surface text-accent font-semibold shadow-xs"
                      : "text-ink-3 hover:text-ink"
                  }`}
                >
                  <span>按关联文章数</span>
                </button>
              </div>
            </div>

            <span className="text-[11.5px] text-ink-4">
              当前展示 {currentTabItems.length} 个标签
            </span>
          </div>
        </div>

        {/* Empty state for 'following' tab when 0 followed */}
        {activeTab === "following" && followedLabels.length === 0 && (
          <div className="card border-dashed p-8 text-center space-y-2">
            <p className="text-[14px] font-semibold text-ink">还没有关注任何标签</p>
            <p className="text-[12px] text-ink-4">
              你可以在「全部标签」中搜索并关注感兴趣的公司、产品或技术主题。
            </p>
            <button
              type="button"
              onClick={() => setActiveTab("all")}
              className="mt-2 inline-flex items-center justify-center rounded-full bg-accent px-4 py-1.5 text-[12px] font-semibold text-accent-contrast hover:bg-accent-ink transition-colors"
            >
              浏览全部标签
            </button>
          </div>
        )}

        {/* Empty search results */}
        {currentTabItems.length === 0 && !(activeTab === "following" && followedLabels.length === 0) && (
          <div className="card border-dashed p-8 text-center text-[13px] text-ink-4">
            没有找到与 “{search}” 匹配的标签。可以尝试输入全称、英文缩写或核心技术词。
          </div>
        )}

        {/* All tags arranged one in each row */}
        {currentTabItems.length > 0 && (
          <fieldset className="space-y-3">
            <legend className="sr-only">
              {activeTab === "following" ? "我关注的标签列表" : "全部标签列表"}
            </legend>
            <div className="space-y-2.5">
              {currentTabItems.map((label) => (
                <TagRow
                  key={label.id}
                  label={label}
                  isFollowed={selected.has(label.id)}
                  onToggle={toggle}
                />
              ))}
            </div>
          </fieldset>
        )}
      </div>
    </section>
  );
}
