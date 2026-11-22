import { BackLink } from "../components/ui/BackLink";
import { useState, useEffect, type FormEvent } from "react";
import { Link, useLoaderData } from "react-router";
import type { Route } from "./+types/sources";
import type { Screen } from "../components/shell/screens";
import { IconChevronRight, IconRss, IconSearch } from "../components/icons";
import { FormMessage, fieldClass } from "../features/account/common";
import { userApiGet, userApiMe } from "../lib/user-api.server";
import { useLocalePreference } from "../lib/local-state";

export const handle: Screen = { tab: "me", name: "我的信源" };

type Subscription = {
  id: number;
  sourceId: string;
  kind?: string;
  name: string;
  nameInherited: boolean;
  updatedExisting?: boolean;
  url: string;
  itemSelector?: string;
  titleSelector?: string;
  enabled: boolean;
  intervalMinutes: number;
  reuseWindowMinutes: number;
  effectiveIntervalMinutes: number;
  effectiveReuseWindowMinutes: number;
  lastCheckedAt: string | null;
  nextFetchAt: string | null;
  health: string;
  filterMode?: "label" | "all";
  tags?: string[];
};

type FetchResult = {
  jobId: number;
  status: "queued" | "running" | "succeeded" | "reused" | "failed" | string;
  checkedAt: string | null;
  foundCount: number;
  newCount: number;
  error?: string;
  items: Array<{ title: string; url: string; publishedAt: string | null }>;
};

type DiscoverCandidate = {
  type: string;
  title: string;
  url: string;
  recommended: boolean;
};

type DiscoverResponse = {
  status: "found" | "not_found";
  originalUrl: string;
  candidates: DiscoverCandidate[];
  error?: string;
};

type ExploreItem = {
  sourceId: string;
  name: string;
  kind: string;
  url: string;
  health: string;
  subscriberCount: number;
  articleCount: number;
  lastCheckedAt: string | null;
  isSubscribed: boolean;
  subscriptionId?: number;
};

type PreviewArticle = {
  id: string;
  title: string;
  url: string;
  author?: string;
  publishedAt: string | null;
  excerpt?: string;
};

type SourcePreviewData = {
  source: ExploreItem;
  articles: PreviewArticle[];
};

/** Load the current user's sources and initial public explore pool. */
export async function loader({ request }: Route.LoaderArgs) {
  const [user, result, explore] = await Promise.all([
    userApiMe(request),
    userApiGet<{ sources: Subscription[] }>(request, "/api/me/sources"),
    userApiGet<{ sources: ExploreItem[] }>(request, "/api/me/sources/explore"),
  ]);

  return {
    csrfToken: user?.csrfToken ?? "",
    sources: result.sources ?? [],
    initialExplore: explore.sources ?? [],
  };
}

export const meta: Route.MetaFunction = () => [{ title: "我的信源 · MyHOT" }, { name: "robots", content: "noindex, nofollow" }];
export const headers: Route.HeadersFunction = () => ({ "Cache-Control": "private, no-store", "X-Robots-Tag": "noindex, nofollow" });

type SourceKind = "rss" | "web_list" | "ingest";

const emptyForm = {
  kind: "rss" as "rss" | "web_list",
  name: "",
  nameInherited: false,
  url: "",
  itemSelector: "",
  titleSelector: "",
  intervalMinutes: 60,
  reuseWindowMinutes: 10,
  enabled: true,
  filterMode: "label" as "label" | "all",
};
type FormValue = typeof emptyForm;

const emptyIngestForm = {
  url: "",
  title: "",
  text: "",
  author: "",
};
type IngestFormValue = typeof emptyIngestForm;

/** Main route for exploring public sources and managing personal source subscriptions. */
export default function SourcesRoute() {
  const initial = useLoaderData<typeof loader>();
  const locale = useLocalePreference();

  const [sources, setSources] = useState<Subscription[]>(initial.sources);
  const [view, setView] = useState<"all" | "following">("all");
  const [sourceKind, setSourceKind] = useState<SourceKind | null>(null);

  // 公共信源探索与模糊搜索
  const [exploreQuery, setExploreQuery] = useState("");
  const [exploreResults, setExploreResults] = useState<ExploreItem[]>(initial.initialExplore);
  const [exploreBusy, setExploreBusy] = useState(false);
  const [exploreError, setExploreError] = useState("");

  // 信源历史收录预览抽屉状态
  const [previewOpen, setPreviewOpen] = useState(false);
  const [previewData, setPreviewData] = useState<SourcePreviewData | null>(null);
  const [previewBusy, setPreviewBusy] = useState(false);
  const [previewError, setPreviewError] = useState("");

  // 一键快速关注弹窗状态
  const [quickTarget, setQuickTarget] = useState<ExploreItem | null>(null);
  const [quickName, setQuickName] = useState("");
  const [quickInterval, setQuickInterval] = useState(60);
  const [quickBusy, setQuickBusy] = useState(false);
  const [quickError, setQuickError] = useState("");

  // 取消关注确认弹窗状态
  const [unfollowTarget, setUnfollowTarget] = useState<Subscription | null>(null);
  const [unfollowBusy, setUnfollowBusy] = useState(false);

  // Handle escape key to close unfollow confirmation modal
  useEffect(() => {
    if (!unfollowTarget) {
      return;
    }

    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !unfollowBusy) {
        setUnfollowTarget(null);
      }
    };

    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
    };
  }, [unfollowTarget, unfollowBusy]);

  // 自定义手动表单状态（用于顶部添加信源）
  const [form, setForm] = useState<FormValue>(emptyForm);
  const [showAdvancedWeb, setShowAdvancedWeb] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");

  // 信源卡片内就地编辑状态（避免划到页面顶部）
  const [editingId, setEditingId] = useState<number | null>(null);
  const [editForm, setEditForm] = useState<FormValue>(emptyForm);
  const [editBusy, setEditBusy] = useState(false);
  const [editError, setEditError] = useState("");
  const [editMessage, setEditMessage] = useState("");
  const [editShowAdvancedWeb, setEditShowAdvancedWeb] = useState(false);
  const [fetching, setFetching] = useState<number | null>(null);
  const [fetchResults, setFetchResults] = useState<Record<number, FetchResult>>({});

  const [discovering, setDiscovering] = useState(false);
  const [discoverResult, setDiscoverResult] = useState<DiscoverCandidate[] | null>(null);
  const [discoverNotice, setDiscoverNotice] = useState("");

  // 已订阅信源卡片展开详情状态（仅允许同时展开一项）
  const [expandedSourceId, setExpandedSourceId] = useState<number | null>(null);

  /** Toggle details visibility for a subscribed source card, ensuring only one is open at a time. */
  function toggleExpand(id: number) {
    setExpandedSourceId(prevId => {
      if (prevId === id) {
        return null;
      }

      return id;
    });
  }

  const [ingestForm, setIngestForm] = useState<IngestFormValue>(emptyIngestForm);
  const [ingestBusy, setIngestBusy] = useState(false);
  const [ingestMsg, setIngestMsg] = useState("");
  const [ingestErr, setIngestErr] = useState("");

  // 监听搜索词变动，执行防抖模糊匹配
  useEffect(() => {
    if (view !== "all") {
      return;
    }

    const controller = new AbortController();
    const timer = setTimeout(() => {
      void searchExplore(exploreQuery, controller.signal);
    }, 300);

    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [exploreQuery, view]);

  /** 执行公共信源模糊检索 */
  async function searchExplore(query: string, signal: AbortSignal) {
    setExploreBusy(true);
    setExploreError("");

    try {
      const q = query.trim();
      const url = q ? `/api/me/sources/explore?q=${encodeURIComponent(q)}` : "/api/me/sources/explore";
      const res = await fetch(url, { signal });
      const data = await res.json() as { sources?: ExploreItem[]; error?: string };
      if (!res.ok) {
        throw new Error(data.error || "检索公共信源失败");
      }

      if (!signal.aborted) {
        setExploreResults(data.sources ?? []);
      }
    } catch (e) {
      if (!signal.aborted) {
        setExploreError(e instanceof Error ? e.message : "检索公共信源失败");
      }
    } finally {
      if (!signal.aborted) {
        setExploreBusy(false);
      }
    }
  }

  /** 打开公共信源历史收录文章预览 */
  async function openPreview(sourceId: string) {
    setPreviewOpen(true);
    setPreviewBusy(true);
    setPreviewError("");
    setPreviewData(null);

    try {
      const res = await fetch(`/api/me/sources/${encodeURIComponent(sourceId)}/preview`);
      const data = await res.json() as SourcePreviewData & { error?: string };
      if (!res.ok) {
        throw new Error(data.error || "获取预览失败");
      }

      setPreviewData(data);
    } catch (e) {
      setPreviewError(e instanceof Error ? e.message : "无法读取信源预览");
    } finally {
      setPreviewBusy(false);
    }
  }

  /** 打开一键关注配置弹窗 */
  function beginQuickSubscribe(item: ExploreItem) {
    setQuickTarget(item);
    setQuickName("");
    setQuickInterval(60);
    setQuickError("");
  }

  /** 提交关注已有信源 */
  async function submitQuickSubscribe(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!quickTarget) {
      return;
    }

    setQuickBusy(true);
    setQuickError("");

    try {
      const res = await fetch("/api/me/sources/subscribe", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": initial.csrfToken,
        },
        body: JSON.stringify({
          sourceId: quickTarget.sourceId,
          name: quickName.trim(),
          intervalMinutes: quickInterval,
          reuseWindowMinutes: 10,
        }),
      });

      const body = await res.json() as Subscription & { error?: string };
      if (!res.ok) {
        throw new Error(body.error || "关注失败");
      }

      // 实时更新已订阅列表
      setSources(prev => [body, ...prev.filter(s => s.sourceId !== body.sourceId)]);

      // 实时标记探索列表与预览窗口中的已关注状态
      setExploreResults(prev => prev.map(item => item.sourceId === quickTarget.sourceId ? {
        ...item,
        isSubscribed: true,
        subscriptionId: body.id,
        subscriberCount: item.subscriberCount + (item.isSubscribed ? 0 : 1),
      } : item));

      if (previewData && previewData.source.sourceId === quickTarget.sourceId) {
        setPreviewData({
          ...previewData,
          source: {
            ...previewData.source,
            isSubscribed: true,
            subscriptionId: body.id,
          },
        });
      }

      setMessage(locale === "en" ? `Subscribed to "${body.name}".` : `已成功关注“${body.name}”，系统已接入历史数据并加入调度！`);
      setQuickTarget(null);
    } catch (err) {
      setQuickError(err instanceof Error ? err.message : "关注失败");
    } finally {
      setQuickBusy(false);
    }
  }

  /** 快捷切换至手动录入模式 */
  function jumpToManual(suggestedKind: "rss" | "web_list" = "rss") {
    chooseSourceKind(suggestedKind);
    if (exploreQuery.trim().startsWith("http://") || exploreQuery.trim().startsWith("https://")) {
      setForm(prev => ({
        ...prev,
        url: exploreQuery.trim(),
      }));
    }
  }

  /** Open inline configuration editor within the selected source card. */
  function beginEdit(source: Subscription) {
    setEditingId(source.id);
    const kind = source.kind === "web_list" ? "web_list" : "rss";
    setEditForm({
      kind,
      name: source.name,
      nameInherited: source.nameInherited ?? false,
      url: source.url,
      itemSelector: source.itemSelector || "",
      titleSelector: source.titleSelector || "",
      intervalMinutes: source.intervalMinutes,
      reuseWindowMinutes: source.reuseWindowMinutes,
      enabled: source.enabled,
      filterMode: source.filterMode ?? "label",
    });
    setEditShowAdvancedWeb(Boolean(source.itemSelector || source.titleSelector));
    setEditError("");
    setEditMessage("");

    // Ensure the card details panel is expanded
    setExpandedSourceId(source.id);
  }

  /** Cancel inline configuration editor and revert to view details. */
  function cancelEdit() {
    setEditingId(null);
    setEditForm(emptyForm);
    setEditError("");
    setEditMessage("");
    setEditShowAdvancedWeb(false);
  }

  /** Reset top add-source form to initial state. */
  function reset() {
    setForm(emptyForm);
    setShowAdvancedWeb(false);
    setDiscoverResult(null);
    setDiscoverNotice("");
    setError("");
    setMessage("");
  }

  /** Choose the source type shown in the add panel. */
  function chooseSourceKind(tab: SourceKind | null) {
    setSourceKind(tab);
    setForm(prev => ({
      ...prev,
      kind: tab === "web_list" ? "web_list" : "rss",
    }));
    setError("");
    setMessage("");
    setIngestMsg("");
    setIngestErr("");
  }

  async function handleDiscover() {
    const target = form.url.trim();
    if (!target) {
      setError(locale === "en" ? "Please enter a website or feed URL" : "请输入需要探测的网址或 Feed 地址");
      return;
    }
    setDiscovering(true);
    setError("");
    setDiscoverNotice("");
    try {
      const response = await fetch("/api/me/sources/discover", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": initial.csrfToken,
        },
        body: JSON.stringify({ url: target }),
      });
      const data = await response.json() as DiscoverResponse;
      if (!response.ok) {
        throw new Error(data.error || (locale === "en" ? "Discovery failed" : "探测失败"));
      }
      if (data.status === "found" && data.candidates?.length > 0) {
        setDiscoverResult(data.candidates);
        setDiscoverNotice(
          locale === "en"
            ? `Found ${data.candidates.length} candidate feed(s). Click to apply:`
            : `成功探测到 ${data.candidates.length} 个可用信源，点击即可直接填入：`
        );
      } else {
        chooseSourceKind("web_list");
        setDiscoverResult([]);
        setDiscoverNotice(
          locale === "en"
            ? "No public feed found. Review the web list settings before saving."
            : "未找到公开订阅源，已切换到网页列表抓取。请检查抓取设置后保存。"
        );
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : (locale === "en" ? "Discovery failed" : "探测失败"));
    } finally {
      setDiscovering(false);
    }
  }

  function applyCandidate(candidate: DiscoverCandidate) {
    setForm(prev => ({
      ...prev,
      kind: "rss",
      url: candidate.url,
      name: prev.name.trim() === "" ? candidate.title : prev.name,
    }));
    setSourceKind("rss");
    setDiscoverNotice(
      locale === "en"
        ? `Applied: ${candidate.title}`
        : `已填入：${candidate.title}`
    );
  }

  /** Submit new subscription from the top form. */
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if ((form.intervalMinutes <= 30 || form.reuseWindowMinutes <= 5) && !window.confirm(locale === "en" ? "High frequency increases server load. Confirm?" : "这会加大服务器压力，确定要这么做吗？")) {
      return;
    }
    setBusy(true);
    setError("");
    setMessage("");

    // Submit new source subscription to backend
    try {
      const payload = {
        kind: form.kind,
        name: form.name.trim(),
        url: form.url.trim(),
        itemSelector: form.kind === "web_list" ? form.itemSelector.trim() : "",
        titleSelector: form.kind === "web_list" ? form.titleSelector.trim() : "",
        intervalMinutes: form.intervalMinutes,
        reuseWindowMinutes: form.reuseWindowMinutes,
        enabled: form.enabled,
        filterMode: form.filterMode,
      };

      const response = await fetch("/api/me/sources", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": initial.csrfToken,
        },
        body: JSON.stringify(payload),
      });

      const body = await response.json() as Subscription & { error?: string };
      if (!response.ok) {
        throw new Error(body.error || (locale === "en" ? "Failed to save source" : "保存信息源失败"));
      }

      setSources(rows => [body, ...rows.filter(row => row.id !== body.id)]);
      setSourceKind(null);
      setView("following");
      setMessage(locale === "en" ? "Subscribed." : "已关注。");
      setForm(emptyForm);
      setDiscoverResult(null);
      setDiscoverNotice("");
    } catch (e) {
      setError(e instanceof Error ? e.message : (locale === "en" ? "Failed to save source" : "保存信息源失败"));
    } finally {
      setBusy(false);
    }
  }

  /** Submit modified configuration for the inline-edited source card. */
  async function saveInline(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!editingId) {
      return;
    }

    if ((editForm.intervalMinutes <= 30 || editForm.reuseWindowMinutes <= 5) && !window.confirm(locale === "en" ? "High frequency increases server load. Confirm?" : "这会加大服务器压力，确定要这么做吗？")) {
      return;
    }

    setEditBusy(true);
    setEditError("");
    setEditMessage("");

    // Submit modified source payload to Go API
    try {
      const payload = {
        kind: editForm.kind,
        name: editForm.name.trim(),
        nameInherited: editForm.nameInherited,
        url: editForm.url.trim(),
        itemSelector: editForm.kind === "web_list" ? editForm.itemSelector.trim() : "",
        titleSelector: editForm.kind === "web_list" ? editForm.titleSelector.trim() : "",
        intervalMinutes: editForm.intervalMinutes,
        reuseWindowMinutes: editForm.reuseWindowMinutes,
        enabled: editForm.enabled,
        filterMode: editForm.filterMode,
      };

      const response = await fetch(`/api/me/sources/${editingId}`, {
        method: "PUT",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": initial.csrfToken,
        },
        body: JSON.stringify(payload),
      });

      const body = await response.json() as Subscription & { error?: string };
      if (!response.ok) {
        throw new Error(body.error || (locale === "en" ? "Failed to save source" : "保存信息源失败"));
      }

      // Update source row in state
      setSources(rows => rows.map(row => row.id === body.id ? body : row));

      // Close inline editor and show feedback
      setEditingId(null);
      setMessage(locale === "en" ? "Source settings updated." : "信源配置已更新。");
    } catch (e) {
      setEditError(e instanceof Error ? e.message : (locale === "en" ? "Failed to save source" : "保存信息源失败"));
    } finally {
      setEditBusy(false);
    }
  }

  async function handleIngest(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!ingestForm.url.trim()) {
      return;
    }
    setIngestBusy(true);
    setIngestMsg("");
    setIngestErr("");
    try {
      const response = await fetch("/api/me/ingest", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": initial.csrfToken,
        },
        body: JSON.stringify({
          url: ingestForm.url.trim(),
          title: ingestForm.title.trim(),
          text: ingestForm.text.trim(),
          author: ingestForm.author.trim(),
        }),
      });
      const data = await response.json() as { ok?: boolean; error?: string; created?: boolean };
      if (!response.ok) {
        throw new Error(data.error || (locale === "en" ? "Failed to ingest article" : "文章投递失败"));
      }
      setIngestMsg(
        locale === "en"
          ? "Article submitted successfully! It is now queued for AI summarization and scoring."
          : "文章投递成功！已加入你的专属处理队列，系统将自动抓取正文并进行 AI 摘要与热点评分。"
      );
      setIngestForm(emptyIngestForm);
      // Refresh source list
      const res = await fetch("/api/me/sources");
      if (res.ok) {
        const body = await res.json() as { sources?: Subscription[] };
        if (body.sources) {
          setSources(body.sources);
        }
      }
    } catch (e) {
      setIngestErr(e instanceof Error ? e.message : (locale === "en" ? "Ingest failed" : "投递失败"));
    } finally {
      setIngestBusy(false);
    }
  }

  async function toggle(source: Subscription) {
    setBusy(true);
    setError("");
    try {
      const body = {
        name: source.name,
        nameInherited: source.nameInherited,
        kind: source.kind,
        url: source.url,
        itemSelector: source.itemSelector,
        titleSelector: source.titleSelector,
        intervalMinutes: source.intervalMinutes,
        reuseWindowMinutes: source.reuseWindowMinutes,
        enabled: !source.enabled,
        filterMode: source.filterMode,
      };
      const response = await fetch(`/api/me/sources/${source.id}`, {
        method: "PATCH",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": initial.csrfToken,
        },
        body: JSON.stringify(body),
      });
      const result = await response.json() as Subscription & { error?: string };
      if (!response.ok) {
        throw new Error(result.error || (locale === "en" ? "Failed to update subscription" : "更新订阅失败"));
      }
      setSources(rows => rows.map(row => row.id === result.id ? result : row));
      if (editingId === result.id) {
        setEditForm(prev => ({ ...prev, enabled: result.enabled }));
      }
      setMessage(result.enabled ? (locale === "en" ? "Subscription resumed." : "订阅已恢复。") : (locale === "en" ? "Subscription paused." : "订阅已暂停。"));
    } catch (e) {
      setError(e instanceof Error ? e.message : (locale === "en" ? "Failed to update subscription" : "更新订阅失败"));
    } finally {
      setBusy(false);
    }
  }

  async function toggleFilterMode(source: Subscription) {
    const nextMode = source.filterMode === "all" ? "label" : "all";
    setBusy(true);
    setError("");
    try {
      const body = {
        name: source.name,
        nameInherited: source.nameInherited,
        kind: source.kind,
        url: source.url,
        itemSelector: source.itemSelector,
        titleSelector: source.titleSelector,
        intervalMinutes: source.intervalMinutes,
        reuseWindowMinutes: source.reuseWindowMinutes,
        enabled: source.enabled,
        filterMode: nextMode,
      };
      const response = await fetch(`/api/me/sources/${source.id}`, {
        method: "PATCH",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": initial.csrfToken,
        },
        body: JSON.stringify(body),
      });
      const result = await response.json() as Subscription & { error?: string };
      if (!response.ok) {
        throw new Error(result.error || (locale === "en" ? "Failed to update filter mode" : "更新过滤策略失败"));
      }
      setSources(rows => rows.map(row => row.id === result.id ? { ...row, filterMode: result.filterMode } : row));
      if (editingId === result.id) {
        setEditForm(prev => ({ ...prev, filterMode: result.filterMode ?? "label" }));
      }
      setMessage(result.filterMode === "all" ? (locale === "en" ? "Switched to Full Tracking." : "已切换为全量跟踪。") : (locale === "en" ? "Switched to Label Filter." : "已切换为按标签过滤。"));
    } catch (e) {
      setError(e instanceof Error ? e.message : (locale === "en" ? "Failed to update filter mode" : "更新过滤策略失败"));
    } finally {
      setBusy(false);
    }
  }

  /** Open the confirmation modal to unfollow a subscription. */
  function remove(source: Subscription) {
    setUnfollowTarget(source);
  }

  /** Confirm and execute unfollow for the selected subscription. */
  async function confirmUnfollow() {
    if (!unfollowTarget) {
      return;
    }

    const source = unfollowTarget;
    setUnfollowBusy(true);
    setError("");

    // Submit unfollow delete request to Go API
    try {
      const response = await fetch(`/api/me/sources/${source.id}`, {
        method: "DELETE",
        headers: {
          "X-CSRF-Token": initial.csrfToken,
        },
      });

      if (!response.ok) {
        const failure = (await response.json().catch(() => null)) as { error?: string } | null;
        throw new Error(failure?.error || (locale === "en" ? "Failed to unfollow source" : "取消关注失败"));
      }

      setSources((rows) => {
        return rows.filter((row) => row.id !== source.id);
      });

      if (editingId === source.id) {
        cancelEdit();
      }

      if (expandedSourceId === source.id) {
        setExpandedSourceId(null);
      }

      // 同步探索列表中的已关注状态
      setExploreResults((prev) => {
        return prev.map((item) => {
          if (item.sourceId === source.sourceId) {
            return {
              ...item,
              isSubscribed: false,
              subscriptionId: undefined,
              subscriberCount: Math.max(0, item.subscriberCount - (item.isSubscribed ? 1 : 0)),
            };
          }
          return item;
        });
      });

      setPreviewData((current) => {
        if (current?.source.sourceId === source.sourceId) {
          return {
            ...current,
            source: {
              ...current.source,
              isSubscribed: false,
              subscriptionId: undefined,
              subscriberCount: Math.max(0, current.source.subscriberCount - (current.source.isSubscribed ? 1 : 0)),
            },
          };
        }
        return current;
      });

      setMessage(locale === "en" ? "Source unfollowed. History kept." : "已取消关注，历史内容已保留。");
      setUnfollowTarget(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : (locale === "en" ? "Failed to unfollow source" : "取消关注失败"));
    } finally {
      setUnfollowBusy(false);
    }
  }

  async function testFetch(source: Subscription) {
    setFetching(source.id);
    setError("");
    setFetchResults(rows => ({
      ...rows,
      [source.id]: {
        jobId: 0,
        status: "queued",
        checkedAt: null,
        foundCount: 0,
        newCount: 0,
        items: [],
      },
    }));
    try {
      const response = await fetch(`/api/me/sources/${source.id}/fetch`, {
        method: "POST",
        headers: {
          "X-CSRF-Token": initial.csrfToken,
        },
      });
      const queued = await response.json() as { jobId?: number; status?: string; error?: string };
      if (!response.ok) {
        if (queued.status === "disabled") {
          throw new Error(locale === "en" ? "Collection is currently disabled." : "采集功能当前已关闭，无法执行测试抓取。");
        }
        throw new Error(queued.error || (locale === "en" ? "Test fetch could not be queued" : "测试抓取未能排队"));
      }
      if (!queued.jobId) {
        throw new Error(locale === "en" ? "Job ID missing" : "任务没有返回编号");
      }
      let latest: FetchResult | undefined;
      for (let attempt = 0; attempt < 45; attempt++) {
        await new Promise(resolve => setTimeout(resolve, 1000));
        const poll = await fetch(`/api/me/sources/${source.id}/fetch/${queued.jobId}`, {
          headers: { "Accept": "application/json" },
        });
        const result = await poll.json() as FetchResult & { error?: string };
        if (!poll.ok) {
          throw new Error(result.error || (locale === "en" ? "Failed to poll fetch status" : "读取抓取状态失败"));
        }
        latest = result;
        setFetchResults(rows => ({ ...rows, [source.id]: result }));
        if (["succeeded", "reused", "failed"].includes(result.status)) {
          break;
        }
      }
      if (latest?.status === "failed") {
        throw new Error(latest.error || (locale === "en" ? "Source fetch failed" : "信源抓取失败"));
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : (locale === "en" ? "Test fetch failed" : "测试抓取失败"));
    } finally {
      setFetching(null);
    }
  }

  const sourceById = new Map(sources.map(source => [source.sourceId, source]));
  const publicIDs = new Set(exploreResults.map(item => item.sourceId));
  const sourceTerm = exploreQuery.trim().toLowerCase();
  const allSources: ExploreItem[] = [
    ...exploreResults,
    ...sources.filter(source => !publicIDs.has(source.sourceId)
      && [source.name, source.url].some(text => text.toLowerCase().includes(sourceTerm))).map(source => ({
      sourceId: source.sourceId,
      name: source.name,
      kind: source.kind ?? "rss",
      url: source.url,
      health: source.health,
      subscriberCount: 0,
      articleCount: 0,
      lastCheckedAt: source.lastCheckedAt,
      isSubscribed: true,
      subscriptionId: source.id,
    })),
  ];
  const previewSubscription = previewData ? sourceById.get(previewData.source.sourceId) : undefined;

  return (
    <main className="mx-auto max-w-[900px] pb-12">
      <BackLink to="/hot" label={locale === "en" ? "Personal Hotspots" : "个人热点榜单"} />

      <div className="mt-3 flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-[25px] font-semibold tracking-tight text-ink">{locale === "en" ? "My Sources" : "我的信源"}</h1>
          <p className="mt-2 max-w-[680px] text-[13px] leading-relaxed text-ink-3">
            {locale === "en"
              ? "Discover and follow public tech sources directly, or manually add RSS feeds, web crawlers, and push Webhooks."
              : "支持直接搜索与一键关注公共信源，或手动添加专属 RSS、网页列表抓取及单篇投递 Webhook。"}
          </p>
        </div>
      </div>

      <div className="mt-6 flex items-end justify-between gap-3 border-b border-line">
        <nav className="flex gap-2" aria-label={locale === "en" ? "Source lists" : "信源列表"}>
          {(["all", "following"] as const).map(option => (
            <button
              key={option}
              type="button"
              aria-pressed={view === option}
              onClick={() => setView(option)}
              className={`whitespace-nowrap border-b-2 px-3 pb-3 text-[13px] font-semibold transition-colors ${view === option
                ? "border-accent text-accent"
                : "border-transparent text-ink-3 hover:text-ink"}`}
            >
              {option === "all"
                ? (locale === "en" ? "All Sources" : "全部信源")
                : (locale === "en" ? `Following (${sources.length})` : `已关注（${sources.length}）`)}
            </button>
          ))}
        </nav>
        <button
          type="button"
          disabled={busy || discovering}
          aria-expanded={sourceKind !== null}
          aria-controls="add-source"
          onClick={() => {
            if (sourceKind === null) {
              jumpToManual();
            } else {
              chooseSourceKind(null);
            }
          }}
          className="mb-2.5 shrink-0 rounded-full bg-accent px-3.5 py-1.5 text-[12px] font-semibold text-accent-contrast hover:bg-accent-ink"
        >
          {sourceKind === null
            ? (locale === "en" ? "+ Add Source" : "＋ 添加信源")
            : (locale === "en" ? "Close" : "收起")}
        </button>
      </div>

      {(error || message) && (
        <div className="mt-4 space-y-2">
          {error && <FormMessage error>{error}</FormMessage>}
          {message && <FormMessage>{message}</FormMessage>}
        </div>
      )}

      {sourceKind !== null && (
        <section id="add-source" className="mt-5" aria-label={locale === "en" ? "Add Source" : "添加信源"}>
          <label htmlFor="source-kind" className="text-[13px] font-semibold text-ink">
            {locale === "en" ? "Source type" : "信源类型"}
          </label>
          <select
            id="source-kind"
            className={fieldClass + " mt-2 max-w-xs"}
            value={sourceKind}
            disabled={busy || discovering || ingestBusy}
            onChange={event => {
              const kind = event.target.value;
              if (kind === "rss" || kind === "web_list" || kind === "ingest") {
                chooseSourceKind(kind);
              }
            }}
          >
            <option value="rss">RSS / Atom</option>
            <option value="web_list">{locale === "en" ? "Web List Crawler" : "网页列表抓取"}</option>
            <option value="ingest">{locale === "en" ? "Direct Ingest / Webhook" : "单篇投递 / Webhook"}</option>
          </select>
      {/* Tab 1 & Tab 2: Regular Sources Form (RSS or Web List) */}
      {(sourceKind === "rss" || sourceKind === "web_list") && (
        <form onSubmit={save} className="card mt-6 p-5 sm:p-6">
          <div className="flex items-center justify-between gap-3">
            <h2 className="text-[15px] font-semibold text-ink">
              {sourceKind === "web_list"
                ? (locale === "en" ? "Add Web List Crawler" : "添加网页列表抓取（普通网页）")
                : (locale === "en" ? "Add RSS / Atom" : "添加 RSS / Atom 订阅")}
            </h2>
          </div>

          {sourceKind === "web_list" && (
            <p className="mt-2 text-[12px] text-ink-3 leading-relaxed">
              {locale === "en"
                ? "Directly crawl any blog archive, news list, or community index page without requiring an RSS feed. The system parses links and automatically updates content."
                : "直接抓取任意没有 RSS 的普通网站列表（如公司官网新闻页、个人博客目录、论坛板块），系统定时解析列表中的文章链接并入库。"}
            </p>
          )}

          <div className="mt-4 grid gap-4 sm:grid-cols-2">
            <div>
              <label className="block text-[12px] font-medium text-ink-2">
                {locale === "en" ? "My source name" : "我的备注名"}
                <input
                  className={`${fieldClass} mt-1.5`}
                  required={!form.nameInherited}
                  disabled={form.nameInherited}
                  maxLength={200}
                  value={form.name}
                  onChange={e => setForm({ ...form, name: e.target.value })}
                  placeholder={sourceKind === "web_list" ? (locale === "en" ? "e.g. OpenAI News" : "例如：OpenAI 官方动态") : (locale === "en" ? "e.g. Tech Blog" : "例如：技术博客")}
                />
              </label>

            </div>

            <div className="sm:col-span-1">
              <label className="text-[12px] font-medium text-ink-2 block">
                {sourceKind === "web_list"
                  ? (locale === "en" ? "Web Page List URL" : "网页列表地址（HTML 页面）")
                  : (locale === "en" ? "RSS / Feed URL" : "信源地址（RSS / Atom / 网站）")}
              </label>
              <div className="mt-1.5 flex gap-2">
                <input
                  className={`${fieldClass} flex-1`}
                  required
                  type="url"
                  maxLength={2048}
                  value={form.url}
                  onChange={e => {
                    setForm({ ...form, url: e.target.value });
                    if (discoverResult) {
                      setDiscoverResult(null);
                    }
                  }}
                  placeholder={sourceKind === "web_list" ? "https://openai.com/news 或 https://example.com/blog" : "https://example.com/feed.xml"}
                />
                {sourceKind === "rss" && (
                  <button
                    type="button"
                    disabled={discovering || busy || !form.url.trim()}
                    onClick={handleDiscover}
                    className="h-10 shrink-0 rounded-control border border-line bg-bg-card px-3.5 text-[12px] font-medium text-ink-2 hover:border-accent hover:text-accent disabled:opacity-40 transition-colors"
                  >
                    {discovering ? (locale === "en" ? "Probing..." : "探测中…") : (locale === "en" ? "Auto-Discover" : "智能探测")}
                  </button>
                )}
              </div>
            </div>

            {/* RSS Candidate Discovery Results */}
            {discoverResult && discoverResult.length > 0 && (
              <div className="sm:col-span-2 rounded-control border border-accent/20 bg-accent-soft/30 p-3">
                <div className="flex items-center justify-between text-[11.5px] font-medium text-accent">
                  <span>{discoverNotice}</span>
                  <button
                    type="button"
                    onClick={() => setDiscoverResult(null)}
                    className="text-ink-4 hover:text-ink text-[12px] px-1"
                    title={locale === "en" ? "Close" : "关闭"}
                  >
                    ✕
                  </button>
                </div>
                <div className="mt-2 grid gap-1.5">
                  {discoverResult.map(c => (
                    <button
                      key={c.url}
                      type="button"
                      onClick={() => applyCandidate(c)}
                      className="flex items-center justify-between rounded-control border border-line bg-surface p-2 text-left text-[12px] hover:border-accent hover:bg-accent-soft/20 transition-colors"
                    >
                      <div className="min-w-0 pr-2">
                        <span className="font-semibold text-ink line-clamp-1">{c.title}</span>
                        <span className="text-[11px] text-ink-4 line-clamp-1">{c.url}</span>
                      </div>
                      <span className="shrink-0 rounded-full bg-accent-soft px-2 py-0.5 text-[10px] font-semibold text-accent">
                        {c.type.toUpperCase()}
                      </span>
                    </button>
                  ))}
                </div>
              </div>
            )}

            {discoverResult && discoverResult.length === 0 && (
              <div className="sm:col-span-2 rounded-control border border-amber-500/20 bg-amber-500/10 p-3 text-[12px] text-amber-800 dark:text-amber-200">
                {discoverNotice}
              </div>
            )}

            {/* Optional CSS Selectors for Web List */}
            {sourceKind === "web_list" && (
              <div className="sm:col-span-2 mt-1">
                <button
                  type="button"
                  onClick={() => setShowAdvancedWeb(!showAdvancedWeb)}
                  className="text-[12px] font-medium text-accent hover:underline flex items-center gap-1"
                >
                  <span>{showAdvancedWeb ? "收起高级 CSS 选择器配置" : "高级配置：自定义 CSS 选择器（可选，默认智能提取）"}</span>
                </button>

                {showAdvancedWeb && (
                  <div className="mt-3 rounded-control border border-line bg-bg-sunk/40 p-3.5 grid gap-3 sm:grid-cols-2">
                    <label className="text-[11.5px] text-ink-2">
                      文章列表项选择器（Item Selector）
                      <input
                        className={`${fieldClass} mt-1`}
                        value={form.itemSelector}
                        onChange={e => setForm({ ...form, itemSelector: e.target.value })}
                        placeholder="例如：article, .news-item, li.post"
                      />
                    </label>
                    <label className="text-[11.5px] text-ink-2">
                      文章标题选择器（Title Selector）
                      <input
                        className={`${fieldClass} mt-1`}
                        value={form.titleSelector}
                        onChange={e => setForm({ ...form, titleSelector: e.target.value })}
                        placeholder="例如：h2 a, .title a, a"
                      />
                    </label>
                  </div>
                )}
              </div>
            )}

            <label className="text-[12px] font-medium text-ink-2">
              {locale === "en" ? "Interval (Minutes)" : "抓取间隔（分钟）"}
              <input
                className={`${fieldClass} mt-1.5`}
                type="number"
                min={1}
                max={1440}
                required
                value={form.intervalMinutes}
                onChange={e => setForm({ ...form, intervalMinutes: Number(e.target.value) })}
              />
            </label>

            <label className="text-[12px] font-medium text-ink-2">
              {locale === "en" ? "Reuse Window (Minutes)" : "复用窗口（分钟）"}
              <input
                className={`${fieldClass} mt-1.5`}
                type="number"
                min={1}
                max={1440}
                required
                value={form.reuseWindowMinutes}
                onChange={e => setForm({ ...form, reuseWindowMinutes: Number(e.target.value) })}
              />
            </label>

          </div>

          <div className="mt-5 flex justify-end gap-3">
            <button
              type="submit"
              disabled={busy}
              className="rounded-full bg-accent px-5 py-2 text-[13px] font-semibold text-accent-contrast hover:bg-accent-ink disabled:opacity-50 transition-colors"
            >
              {busy ? (locale === "en" ? "Saving..." : "保存中…") : (locale === "en" ? "Add Source" : "保存信源")}
            </button>
          </div>
        </form>
      )}

      {/* Tab 3: Direct Ingest Article & Personal Webhook */}
      {sourceKind === "ingest" && (
        <div className="mt-6 space-y-6">
          <form onSubmit={handleIngest} className="card p-5 sm:p-6">
            <h2 className="text-[15px] font-semibold text-ink">
              {locale === "en" ? "Quick Direct Ingest" : "单篇快速直接投递"}
            </h2>
            <p className="mt-1.5 text-[12px] text-ink-3 leading-relaxed">
              {locale === "en"
                ? "Manually submit any online article directly. The platform will fetch its body, summarize it via your AI model, and match against your interests."
                : "遇到感兴趣的单篇好文？直接粘贴网址和标题投递入库，系统将抓取正文并由你的私有模型生成专属摘要。"}
            </p>

            {(ingestErr || ingestMsg) && (
              <div className="mt-4 space-y-2">
                {ingestErr && <FormMessage error>{ingestErr}</FormMessage>}
                {ingestMsg && <FormMessage>{ingestMsg}</FormMessage>}
              </div>
            )}

            <div className="mt-4 grid gap-4">
              <label className="text-[12px] font-medium text-ink-2">
                {locale === "en" ? "Article URL *" : "文章原文网址（必填）*"}
                <input
                  className={`${fieldClass} mt-1.5`}
                  required
                  type="url"
                  maxLength={2048}
                  value={ingestForm.url}
                  onChange={e => setIngestForm({ ...ingestForm, url: e.target.value })}
                  placeholder="https://example.com/great-article"
                />
              </label>

              <div className="grid gap-4 sm:grid-cols-2">
                <label className="text-[12px] font-medium text-ink-2">
                  {locale === "en" ? "Article Title (Optional)" : "文章标题（选填，留空将自动抓取）"}
                  <input
                    className={`${fieldClass} mt-1.5`}
                    maxLength={256}
                    value={ingestForm.title}
                    onChange={e => setIngestForm({ ...ingestForm, title: e.target.value })}
                    placeholder="不填则自动从网页解析标题"
                  />
                </label>

                <label className="text-[12px] font-medium text-ink-2">
                  {locale === "en" ? "Author / Source (Optional)" : "作者或来源（选填）"}
                  <input
                    className={`${fieldClass} mt-1.5`}
                    maxLength={100}
                    value={ingestForm.author}
                    onChange={e => setIngestForm({ ...ingestForm, author: e.target.value })}
                    placeholder="例如：Paul Graham"
                  />
                </label>
              </div>

              <label className="text-[12px] font-medium text-ink-2">
                {locale === "en" ? "Custom Article Content / Excerpt (Optional)" : "自定义正文或摘要（选填，留空将自动抓取原文正文）"}
                <textarea
                  className={`${fieldClass} mt-1.5 min-h-[90px]`}
                  value={ingestForm.text}
                  onChange={e => setIngestForm({ ...ingestForm, text: e.target.value })}
                  placeholder="如果该网页需要登录或有反爬限制，可将正文或核心摘要直接粘贴到这里…"
                />
              </label>
            </div>

            <div className="mt-5 flex justify-end">
              <button
                type="submit"
                disabled={ingestBusy || !ingestForm.url.trim()}
                className="rounded-full bg-accent px-5 py-2 text-[13px] font-semibold text-accent-contrast hover:bg-accent-ink disabled:opacity-50 transition-colors"
              >
                {ingestBusy ? (locale === "en" ? "Submitting..." : "投递中…") : (locale === "en" ? "Submit Article" : "立即投递文章")}
              </button>
            </div>
          </form>

          {/* Webhook & Automation Guide */}
          <div className="card p-5 sm:p-6 bg-bg-sunk/30">
            <h3 className="text-[14px] font-semibold text-ink flex items-center gap-2">
              <span>⚡</span>
              <span>{locale === "en" ? "Personal Ingest Webhook API" : "专属 Webhook 自动化接口"}</span>
            </h3>
            <p className="mt-2 text-[12px] text-ink-3 leading-relaxed">
              {locale === "en"
                ? "You can easily push articles from browser extensions, iOS Shortcuts, n8n, or Zapier using the standard HTTP POST endpoint:"
                : "你可以配合油猴剪藏脚本、iOS 快捷指令、Zapier 或 n8n 自动化流程，通过 HTTP POST 请求将任意文章一键推送到 MyHOT："}
            </p>

            <div className="mt-3 overflow-x-auto rounded-control bg-bg p-3.5 mono text-[11px] text-ink-2 border border-line">
              <pre className="whitespace-pre-wrap">{`curl -X POST "${typeof window !== "undefined" ? window.location.origin : ""}/api/me/ingest" \\
  -H "Content-Type: application/json" \\
  -H "X-CSRF-Token: ${initial.csrfToken}" \\
  -d '{"url": "https://example.com/article", "title": "文章标题"}'`}</pre>
            </div>
            <p className="mt-2 text-[11px] text-ink-4">
              {locale === "en"
                ? "Tip: The system automatically deduplicates by URL. Discovered content is processed through your private model configuration."
                : "提示：相同 URL 会自动去重；入库后会自动按你的个人关键词筛选并调用你配置的模型生成 AI 摘要。"}
            </p>
          </div>
        </div>
      )}

        </section>
      )}

      {/* Tab 0: Public Source Exploration & Discovery */}
      {view === "all" && (
        <section className="mt-6">
          {/* Search Bar */}
          <div className="card p-4 sm:p-5">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h2 className="text-[15px] font-semibold text-ink flex items-center gap-2">
                <span>{locale === "en" ? "Public Source Marketplace" : "信源探索广场"}</span>
              </h2>

            </div>
            <p className="mt-1 text-[12px] text-ink-3">
              {locale === "en"
                ? "Search existing sources by name, keyword, or URL. Preview recent articles before following."
                : "支持模糊搜索信源名称、关键词或域名。点击可查看收录历史与最新文章，一键关注直接复用。"}
            </p>

            <div className="mt-3 relative flex items-center">
              <IconSearch size={15} className="absolute left-3.5 text-ink-4" />
              <input
                className={`${fieldClass} pl-9 pr-9 h-11 text-[13px]`}
                value={exploreQuery}
                onChange={e => setExploreQuery(e.target.value)}
                placeholder={locale === "en" ? "Search sources (e.g. OpenAI, 36Kr, Hacker News, tech blogs)..." : "搜索信源名称、网址、关键词（如：OpenAI、知乎、36氪、Hacker News...）"}
              />
              {exploreQuery && (
                <button
                  type="button"
                  onClick={() => setExploreQuery("")}
                  className="absolute right-3 text-ink-4 hover:text-ink text-[13px] px-1"
                  title="清空搜索"
                >
                  ✕
                </button>
              )}
            </div>

            <div className="mt-2.5 flex items-center justify-between text-[11px] text-ink-4">
              <span>{exploreBusy ? "正在搜索…" : exploreQuery.trim() ? `搜索结果（${allSources.length} 个匹配）` : "平台热门与活跃信源推荐"}</span>
              <span className="hidden sm:inline">关注后即刻同步已有收录历史</span>
            </div>
          </div>

          {exploreError && <div className="mt-3"><FormMessage error>{exploreError}</FormMessage></div>}
          {/* Source rows */}
          {allSources.length === 0 ? (
            <div className="card mt-4 border-dashed p-8 text-center">
              <p className="text-[14px] font-semibold text-ink">
                {locale === "en" ? "No matching public sources found" : "没有找到匹配的公共信源"}
              </p>
              <p className="mt-1.5 text-[12px] text-ink-3">
                {locale === "en"
                  ? "You can be the first to add this source! Support RSS/Atom feeds or regular Web List Crawlers."
                  : "你可以成为第一个添加该信源的人！系统将为你开启定时采集与智能摘要。"}
              </p>

            </div>
          ) : (
            <div className="mt-4 space-y-3">
              {allSources.map(item => {
                const subscription = sourceById.get(item.sourceId);
                const isWebList = item.kind === "web_list";
                const isIngest = item.kind === "external";
                const publicResult = publicIDs.has(item.sourceId);
                return (
                  <article key={item.sourceId} className="card flex flex-col justify-between gap-3 p-4 transition-colors hover:border-accent/40 sm:flex-row sm:items-center sm:p-5">
                    <div className="min-w-0 flex-1">
                      <div className="flex items-start justify-between gap-2">
                        <h3 className="text-[14px] font-bold text-ink leading-snug line-clamp-1" title={item.name}>
                          {item.name}
                        </h3>
                        <span className={`shrink-0 rounded-full px-2 py-0.5 text-[10px] font-semibold ${isWebList ? "bg-blue-100 text-blue-700 dark:bg-blue-950 dark:text-blue-300" : "bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300"}`}>
                          {isIngest ? "Webhook" : isWebList ? "网页列表" : "RSS / Atom"}
                        </span>
                      </div>

                      {item.url && <a
                        href={item.url}
                        target="_blank"
                        rel="noreferrer"
                        className="mt-1.5 block max-w-full truncate text-[11px] text-ink-4 hover:text-accent"
                        title={item.url}
                      >
                        {item.url} ↗
                      </a>}

                      <div className="mt-3 flex flex-wrap items-center gap-2 text-[11px] text-ink-3">
                        <span className={`inline-flex items-center gap-1 rounded-full px-2 py-0.5 ${item.health === "ok" ? "bg-emerald-50 text-emerald-700 dark:bg-emerald-950/60 dark:text-emerald-300" : "bg-bg-sunk text-ink-4"}`}>
                          <span className={`size-1.5 rounded-full ${item.health === "ok" ? "bg-emerald-500" : "bg-ink-4"}`} />
                          {item.health === "ok" ? "活跃运行" : "探测中"}
                        </span>
                        {publicResult && (
                          <span>· {item.subscriberCount} 人关注 · 收录 {item.articleCount} 篇</span>
                        )}
                      </div>
                    </div>

                    <div className="flex shrink-0 items-center justify-between gap-3 border-t border-line pt-3 sm:border-0 sm:pt-0">
                      {!isIngest && <button
                        type="button"
                        onClick={() => openPreview(item.sourceId)}
                        className="text-[12px] font-medium text-ink-3 hover:text-accent transition-colors flex items-center gap-1"
                      >
                        <span>历史预览</span>
                        <span>↗</span>
                      </button>}

                      {subscription ? (
                        <button type="button" disabled={busy || fetching === subscription.id || editBusy} onClick={() => void remove(subscription)}
                          className="ml-auto rounded-full border border-line px-3 py-1 text-[12px] text-ink-3 hover:text-red-600 disabled:opacity-50">
                          {locale === "en" ? "Unfollow" : "取消关注"}
                        </button>
                      ) : (
                        <button
                          type="button"
                          onClick={() => beginQuickSubscribe(item)}
                          className="rounded-full bg-accent px-3.5 py-1 text-[12px] font-semibold text-accent-contrast hover:bg-accent-ink transition-colors"
                        >
                          + 关注
                        </button>
                      )}
                    </div>
                  </article>
                );
              })}
            </div>
          )}
        </section>
      )}

      {/* Subscribed Sources List */}
      {view === "following" && <section aria-label={locale === "en" ? "Following" : "已关注信源"} className="mt-6">
        <div className="flex items-center justify-between">
          <h2 className="text-[16px] font-bold text-ink">
            {locale === "en" ? "My Subscribed Sources" : "已订阅信源列表"}
          </h2>
        </div>

        {sources.length === 0 ? (
          <div className="card mt-3 border-dashed p-6 text-center text-[13px] text-ink-4">
            {locale === "en"
              ? "No subscriptions yet. Browse All Sources to follow one."
              : "还没有关注信源。可切换到「全部信源」浏览，或添加新信源。"}
          </div>
        ) : (
          <div className="mt-3 grid gap-3">
            {sources.map(source => {
              const result = fetchResults[source.id];
              const isWebList = source.kind === "web_list";
              const isIngest = source.kind === "external";
              const isExpanded = expandedSourceId === source.id;

              return (
                <article className="card relative p-4 sm:p-5 transition-all" key={source.id}>
                  <button type="button" disabled={busy || fetching === source.id || editBusy} onClick={() => void remove(source)}
                    className="absolute right-4 top-4 rounded-full border border-line px-3 py-1 text-[12px] text-ink-3 hover:text-red-600 disabled:opacity-50 sm:right-5 sm:top-5">
                    {locale === "en" ? "Unfollow" : "取消关注"}
                  </button>
                  {/* 信源列表卡片头部概要（点击展开/收起详情） */}
                  <div
                    className="group w-full cursor-pointer select-none pr-24"
                    onClick={() => toggleExpand(source.id)}
                    role="button"
                    tabIndex={0}
                    onKeyDown={e => {
                      if (e.key === "Enter" || e.key === " ") {
                        e.preventDefault();
                        toggleExpand(source.id);
                      }
                    }}
                    aria-expanded={isExpanded}
                  >
                    <div className="flex flex-wrap items-center gap-2">
                      <IconChevronRight
                        size={15}
                        className={`shrink-0 transition-transform duration-200 ${
                          isExpanded ? "rotate-90 text-accent" : "text-ink-4 group-hover:text-accent"
                        }`}
                      />
                      <h3 className="text-[14px] font-semibold text-ink group-hover:text-accent transition-colors">
                        {source.name}
                      </h3>
                      {isWebList ? (
                        <span className="rounded-full bg-blue-100 text-blue-700 dark:bg-blue-950 dark:text-blue-300 px-2 py-0.5 text-[10px] font-semibold">
                          {locale === "en" ? "Web List" : "网页列表"}
                        </span>
                      ) : isIngest ? (
                        <span className="rounded-full bg-purple-100 text-purple-700 dark:bg-purple-950 dark:text-purple-300 px-2 py-0.5 text-[10px] font-semibold">
                          {locale === "en" ? "Direct Ingest" : "手动投递"}
                        </span>
                      ) : (
                        <span className="rounded-full bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300 px-2 py-0.5 text-[10px] font-semibold">
                          RSS / Atom
                        </span>
                      )}
                      {!source.enabled && (
                        <span className="rounded-full bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300 px-2 py-0.5 text-[10px] font-semibold">
                          {locale === "en" ? "Paused" : "已暂停"}
                        </span>
                      )}
                    </div>

                    {source.tags && source.tags.length > 0 && (
                      <div className="mt-2 flex flex-wrap items-center gap-1.5 pl-[23px]">
                        <span className="text-[10.5px] text-ink-4 flex items-center gap-1 mr-0.5">
                          <span>{source.enabled && source.filterMode !== "all"
                            ? (locale === "en" ? "Automatically followed:" : "自动关注：")
                            : (locale === "en" ? "Source tags:" : "信源标签：")}</span>
                        </span>
                        {source.tags.map((tag) => (
                          <span
                            key={tag}
                            className="inline-flex items-center rounded-md bg-surface-2 px-2 py-0.5 text-[10.5px] font-medium text-ink-2 border border-line/60"
                          >
                            {tag}
                          </span>
                        ))}
                      </div>
                    )}

                    <p className="mt-1.5 pl-[23px] text-[11px] text-ink-4">
                      {locale === "en"
                        ? `Last checked: ${source.lastCheckedAt ? new Date(source.lastCheckedAt).toLocaleString() : "Never"}${source.nextFetchAt ? ` · Next fetch: ${new Date(source.nextFetchAt).toLocaleString()}` : ""}`
                        : `最近检查：${source.lastCheckedAt ? new Date(source.lastCheckedAt).toLocaleString() : "尚未成功检查"}${source.nextFetchAt ? ` · 下次自动抓取 ${new Date(source.nextFetchAt).toLocaleString()}` : ""}`}
                    </p>
                  </div>

                  {/* 展开的信源详细信息面板 / 卡片内就地编辑表单 */}
                  {isExpanded && (
                    editingId === source.id ? (
                      <form
                        onSubmit={saveInline}
                        className="mt-4 rounded-control border border-line/70 bg-bg-sunk/40 p-4 sm:p-5 text-[12px] space-y-4"
                      >
                        <div className="flex items-center justify-between border-b border-line/60 pb-3">
                          <h4 className="text-[14px] font-semibold text-ink flex items-center gap-2">
                            <span>{locale === "en" ? "Edit Source Configuration" : "编辑信源配置"}</span>
                            <span className="text-[12px] font-normal text-ink-3">({source.name})</span>
                          </h4>
                          <button
                            type="button"
                            onClick={cancelEdit}
                            className="text-[12px] text-ink-4 hover:text-accent transition-colors"
                          >
                            {locale === "en" ? "Cancel" : "取消"}
                          </button>
                        </div>

                        <div className="grid gap-4 sm:grid-cols-2">
                          <div>
                            <label className="block text-[12px] font-medium text-ink-2">
                              {locale === "en" ? "My source name" : "我的备注名"}
                              <input
                                className={`${fieldClass} mt-1.5`}
                                required={!editForm.nameInherited}
                                disabled={editForm.nameInherited}
                                maxLength={200}
                                value={editForm.name}
                                onChange={(e) => {
                                  setEditForm({ ...editForm, name: e.target.value });
                                }}
                                placeholder={locale === "en" ? "Source Name" : "信源备注名"}
                              />
                            </label>
                            <label className="mt-2 flex items-center gap-2 text-[12px] text-ink-3">
                              <input
                                type="checkbox"
                                className="accent-accent"
                                checked={editForm.nameInherited}
                                onChange={(e) => {
                                  setEditForm({ ...editForm, nameInherited: e.target.checked });
                                }}
                              />
                              {locale === "en" ? "Use shared source name" : "使用共享信源名"}
                            </label>
                          </div>

                          <div>
                            <label className="text-[12px] font-medium text-ink-2 block">
                              {source.kind === "web_list"
                                ? locale === "en"
                                  ? "Web Page List URL"
                                  : "网页列表地址（HTML 页面）"
                                : locale === "en"
                                ? "RSS / Feed URL"
                                : "信源地址（RSS / Atom / 网站）"}
                              <input
                                className={`${fieldClass} mt-1.5`}
                                required
                                type="url"
                                maxLength={2048}
                                value={editForm.url}
                                onChange={(e) => {
                                  setEditForm({ ...editForm, url: e.target.value });
                                }}
                              />
                            </label>
                          </div>

                          {/* Optional CSS Selectors for Web List */}
                          {source.kind === "web_list" && (
                            <div className="sm:col-span-2 mt-1">
                              <button
                                type="button"
                                onClick={() => {
                                  setEditShowAdvancedWeb(!editShowAdvancedWeb);
                                }}
                                className="text-[12px] font-medium text-accent hover:underline flex items-center gap-1"
                              >
                                <span>
                                  {editShowAdvancedWeb
                                    ? locale === "en"
                                      ? "Hide Advanced CSS Selectors"
                                      : "收起高级 CSS 选择器配置"
                                    : locale === "en"
                                    ? "Advanced: Custom CSS Selectors (Optional)"
                                    : "高级配置：自定义 CSS 选择器（可选，默认智能提取）"}
                                </span>
                              </button>

                              {editShowAdvancedWeb && (
                                <div className="mt-3 rounded-control border border-line bg-surface p-3.5 grid gap-3 sm:grid-cols-2">
                                  <label className="text-[11.5px] text-ink-2">
                                    {locale === "en" ? "Item Selector" : "文章列表项选择器（Item Selector）"}
                                    <input
                                      className={`${fieldClass} mt-1`}
                                      value={editForm.itemSelector}
                                      onChange={(e) => {
                                        setEditForm({ ...editForm, itemSelector: e.target.value });
                                      }}
                                      placeholder="例如：article, .news-item, li.post"
                                    />
                                  </label>
                                  <label className="text-[11.5px] text-ink-2">
                                    {locale === "en" ? "Title Selector" : "文章标题选择器（Title Selector）"}
                                    <input
                                      className={`${fieldClass} mt-1`}
                                      value={editForm.titleSelector}
                                      onChange={(e) => {
                                        setEditForm({ ...editForm, titleSelector: e.target.value });
                                      }}
                                      placeholder="例如：h2 a, .title a, a"
                                    />
                                  </label>
                                </div>
                              )}
                            </div>
                          )}

                          <label className="text-[12px] font-medium text-ink-2">
                            {locale === "en" ? "Interval (Minutes)" : "抓取间隔（分钟）"}
                            <input
                              className={`${fieldClass} mt-1.5`}
                              type="number"
                              min={1}
                              max={1440}
                              required
                              value={editForm.intervalMinutes}
                              onChange={(e) => {
                                setEditForm({ ...editForm, intervalMinutes: Number(e.target.value) });
                              }}
                            />
                          </label>

                          <label className="text-[12px] font-medium text-ink-2">
                            {locale === "en" ? "Reuse Window (Minutes)" : "复用窗口（分钟）"}
                            <input
                              className={`${fieldClass} mt-1.5`}
                              type="number"
                              min={1}
                              max={1440}
                              required
                              value={editForm.reuseWindowMinutes}
                              onChange={(e) => {
                                setEditForm({ ...editForm, reuseWindowMinutes: Number(e.target.value) });
                              }}
                            />
                          </label>

                          <label className="sm:col-span-2 flex items-center gap-2 text-[12px] text-ink-2">
                            <input
                              type="checkbox"
                              className="accent-accent"
                              checked={editForm.enabled}
                              onChange={(e) => {
                                setEditForm({ ...editForm, enabled: e.target.checked });
                              }}
                            />
                            {locale === "en" ? "Enable this subscription" : "启用此订阅"}
                          </label>
                        </div>

                        {editError && (
                          <FormMessage error>{editError}</FormMessage>
                        )}
                        {editMessage && (
                          <FormMessage>{editMessage}</FormMessage>
                        )}

                        <div className="flex justify-end gap-3 pt-3 border-t border-line/60">
                          <button
                            type="button"
                            disabled={editBusy}
                            onClick={cancelEdit}
                            className="rounded-full border border-line px-4 py-1.5 text-[12px] font-semibold text-ink-3 hover:border-accent transition-colors disabled:opacity-50"
                          >
                            {locale === "en" ? "Cancel" : "取消"}
                          </button>
                          <button
                            type="submit"
                            disabled={editBusy}
                            className="rounded-full bg-accent px-5 py-1.5 text-[12px] font-semibold text-accent-contrast hover:bg-accent-ink transition-colors disabled:opacity-50"
                          >
                            {editBusy
                              ? locale === "en"
                                ? "Saving..."
                                : "保存中…"
                              : locale === "en"
                              ? "Save Changes"
                              : "保存修改"}
                          </button>
                        </div>
                      </form>
                    ) : (
                      <div className="mt-4 rounded-control border border-line/70 bg-bg-sunk/40 p-4 text-[12px] space-y-3">
                        <div className="flex flex-wrap items-center justify-between gap-3 pb-3 border-b border-line/60">
                            <div className="min-w-0 flex-1">
                              <span className="text-[11px] font-medium text-ink-4 block">
                                {locale === "en" ? "Source URL" : "信源完整地址"}
                              </span>
                              <span className="mono mt-0.5 block break-all text-[11.5px] text-ink-2 select-all">
                                {source.url}
                              </span>
                            </div>

                            <div className="flex shrink-0 items-center gap-2">
                              <a
                                href={source.url}
                                target="_blank"
                                rel="noreferrer"
                                className="inline-flex items-center gap-1.5 rounded-full bg-accent px-3 py-1 text-[11px] font-semibold text-accent-contrast hover:bg-accent-ink transition-colors"
                              >
                                <span>{locale === "en" ? "Visit Website" : "访问信源网页"}</span>
                                <span>↗</span>
                              </a>

                              {!isIngest && (
                                <button
                                  type="button"
                                  onClick={() => openPreview(source.sourceId)}
                                  className="inline-flex items-center gap-1 rounded-full border border-line bg-surface px-3 py-1 text-[11px] font-medium text-ink-2 hover:border-accent hover:text-accent transition-colors"
                                >
                                  <span>{locale === "en" ? "Browse Ingested" : "查看收录历史"}</span>
                                  <span>↗</span>
                                </button>
                              )}
                            </div>
                          </div>

                          {!isIngest && (
                            <div className="grid grid-cols-1 sm:grid-cols-2 gap-2 text-[11.5px] text-ink-3">
                              <div className="flex items-center justify-between rounded bg-surface/80 px-2.5 py-1.5 border border-line/40">
                                <span className="text-ink-4">{locale === "en" ? "Your Fetch Interval" : "你的抓取频率"}</span>
                                <span className="font-medium text-ink">{locale === "en" ? `Every ${source.intervalMinutes}m` : `每 ${source.intervalMinutes} 分钟`}</span>
                              </div>

                              <div className="flex items-center justify-between rounded bg-surface/80 px-2.5 py-1.5 border border-line/40">
                                <span className="text-ink-4">{locale === "en" ? "Reuse Window" : "结果复用窗口"}</span>
                                <span className="font-medium text-ink">{locale === "en" ? `${source.reuseWindowMinutes}m` : `${source.reuseWindowMinutes} 分钟`}</span>
                              </div>

                              <div className="flex items-center justify-between rounded bg-surface/80 px-2.5 py-1.5 border border-line/40">
                                <span className="text-ink-4">{locale === "en" ? "Cluster Effective Interval" : "集群聚合有效周期"}</span>
                                <span className="font-medium text-ink">{locale === "en" ? `Every ${source.effectiveIntervalMinutes}m` : `每 ${source.effectiveIntervalMinutes} 分钟`}</span>
                              </div>

                              <div className="flex items-center justify-between rounded bg-surface/80 px-2.5 py-1.5 border border-line/40">
                                <span className="text-ink-4">{locale === "en" ? "Health Status" : "健康状态"}</span>
                                <span className={`font-medium ${source.health === "ok" ? "text-emerald-600 dark:text-emerald-400" : "text-amber-600 dark:text-amber-400"}`}>
                                  {source.health === "ok" ? (locale === "en" ? "Healthy" : "正常运行") : source.health}
                                </span>
                              </div>
                            </div>
                          )}

                          {isWebList && (source.itemSelector || source.titleSelector) && (
                            <div className="rounded bg-surface/80 p-2.5 border border-line/40 text-[11px] space-y-1">
                              {source.itemSelector && (
                                <div>
                                  <span className="text-ink-4">{locale === "en" ? "Item Selector: " : "条目选择器："}</span>
                                  <code className="mono text-ink-2">{source.itemSelector}</code>
                                </div>
                              )}
                              {source.titleSelector && (
                                <div>
                                  <span className="text-ink-4">{locale === "en" ? "Title Selector: " : "标题选择器："}</span>
                                  <code className="mono text-ink-2">{source.titleSelector}</code>
                                </div>
                              )}
                            </div>
                          )}

                          {/* 信源操作按钮工具栏 */}
                          <div className="flex flex-wrap items-center justify-between gap-2.5 pt-3 border-t border-line/60">
                            <div className="flex flex-wrap items-center gap-2">
                              {!isIngest && (
                                <button
                                  type="button"
                                  disabled={busy || fetching === source.id}
                                  onClick={() => void testFetch(source)}
                                  className="h-8 rounded-full bg-accent px-3.5 text-[11px] font-semibold text-accent-contrast hover:bg-accent-ink transition-colors disabled:opacity-50"
                                >
                                  {fetching === source.id
                                    ? (locale === "en" ? "Testing..." : "正在测试…")
                                    : (locale === "en" ? "Test Fetch" : "测试抓取")}
                                </button>
                              )}

                              {!isIngest && (
                                <button
                                  type="button"
                                  disabled={busy}
                                  onClick={() => beginEdit(source)}
                                  className="h-8 rounded-full border border-line bg-surface px-3.5 text-[11px] font-medium text-ink-2 hover:border-accent/40 hover:text-accent transition-colors disabled:opacity-50"
                                >
                                  {locale === "en" ? "Edit" : "编辑"}
                                </button>
                              )}

                              <button
                                type="button"
                                disabled={busy}
                                onClick={() => void toggleFilterMode(source)}
                                title={
                                  source.filterMode === "all"
                                    ? (locale === "en"
                                        ? "Full Tracking: all articles are evaluated. Click to switch to Label Filter."
                                        : "当前为全量跟踪：该信源所有新文章均进入精选打分。点击切换为按标签过滤。")
                                    : (locale === "en"
                                        ? "Matches automatically followed source tags or manual interests. When no stable source tags are available, articles continue to model selection."
                                        : "匹配该信源自动关注的标签或手动关注项。标签尚未识别时，直接进入后续筛选。点击切换为全量跟踪。")
                                }
                                className={`h-8 rounded-full border px-3.5 text-[11px] font-medium transition-colors disabled:opacity-50 ${
                                  source.filterMode === "all"
                                    ? "border-accent/40 bg-accent-soft text-accent"
                                    : "border-line bg-surface text-ink-3 hover:border-line-strong hover:text-ink"
                                }`}
                              >
                                {source.filterMode === "all"
                                  ? (locale === "en" ? "Full Tracking" : "全量跟踪")
                                  : (locale === "en" ? "Label Filter" : "标签过滤")}
                              </button>

                              <button
                                type="button"
                                disabled={busy}
                                onClick={() => void toggle(source)}
                                className="h-8 rounded-full border border-line bg-surface px-3.5 text-[11px] font-medium text-ink-2 hover:border-accent/40 hover:text-accent transition-colors disabled:opacity-50"
                              >
                                {source.enabled
                                  ? (locale === "en" ? "Pause" : "暂停")
                                  : (locale === "en" ? "Resume" : "恢复")}
                              </button>
                            </div>

                          </div>
                        </div>
                      )
                  )}

                  {result && (
                    <div className="mt-4 rounded-control bg-bg-sunk p-3 text-[11px] text-ink-3">
                      <p>
                        {result.status === "queued" ? (
                          locale === "en" ? "Fetch queued" : "抓取已排队"
                        ) : result.status === "running" ? (
                          locale === "en" ? "Fetching..." : "正在抓取"
                        ) : result.status === "reused" ? (
                          locale === "en" ? "Reused recent successful fetch" : "复用了近期成功结果"
                        ) : result.status === "failed" ? (
                          locale === "en" ? `Fetch failed: ${result.error ?? "Unknown error"}` : `抓取失败：${result.error ?? "未知错误"}`
                        ) : (
                          locale === "en"
                            ? `Checked${result.checkedAt ? ` · ${new Date(result.checkedAt).toLocaleString()}` : ""} · Found ${result.foundCount} · New ${result.newCount}`
                            : `检查完成${result.checkedAt ? ` · ${new Date(result.checkedAt).toLocaleString()}` : ""} · 读取 ${result.foundCount} 条 · 新增 ${result.newCount} 条`
                        )}
                      </p>
                      {result.items?.length > 0 && (
                        <ul className="mt-2 space-y-1.5">
                          {result.items.slice(0, 20).map((item, index) => (
                            <li key={`${item.url}-${index}`}>
                              <a className="line-clamp-1 hover:text-accent" href={item.url} target="_blank" rel="noreferrer">
                                {item.title}
                              </a>
                            </li>
                          ))}
                        </ul>
                      )}
                    </div>
                  )}
                </article>
              );
            })}
          </div>
        )}
      </section>}

      {/* 信源历史文章预览抽屉/弹窗 */}
      {previewOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4 backdrop-blur-xs">
          <div className="card max-h-[85vh] w-full max-w-[640px] flex flex-col p-5 sm:p-6 overflow-hidden">
            <div className="flex items-start justify-between gap-3 border-b border-line pb-4">
              <div className="min-w-0">
                <h3 className="text-[17px] font-bold text-ink flex items-center gap-2">
                  <span>{previewData?.source.name || "信源预览"}</span>
                  {previewData?.source.kind === "web_list" ? (
                    <span className="rounded-full bg-blue-100 text-blue-700 dark:bg-blue-950 dark:text-blue-300 px-2 py-0.5 text-[10px] font-semibold">
                      网页列表
                    </span>
                  ) : (
                    <span className="rounded-full bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300 px-2 py-0.5 text-[10px] font-semibold">
                      RSS
                    </span>
                  )}
                </h3>
                {previewData?.source.url && (
                  <a
                    href={previewData.source.url}
                    target="_blank"
                    rel="noreferrer"
                    className="mt-1 block text-[11px] text-ink-4 hover:text-accent truncate"
                  >
                    {previewData.source.url} ↗
                  </a>
                )}
              </div>

              <div className="flex items-center gap-2 shrink-0">
                {previewData?.source && !previewSubscription && (
                  <button
                    type="button"
                    onClick={() => {
                      setPreviewOpen(false);
                      beginQuickSubscribe(previewData.source);
                    }}
                    className="rounded-full bg-accent px-3.5 py-1 text-[12px] font-semibold text-accent-contrast hover:bg-accent-ink transition-colors"
                  >
                    + 关注此信源
                  </button>
                )}
                {previewSubscription && (
                  <button type="button" disabled={busy || fetching === previewSubscription.id || editBusy} onClick={() => void remove(previewSubscription)}
                    className="rounded-full border border-line px-3 py-1 text-[12px] text-ink-3 hover:text-red-600 disabled:opacity-50">
                    {locale === "en" ? "Unfollow" : "取消关注"}
                  </button>
                )}
                <button
                  type="button"
                  onClick={() => setPreviewOpen(false)}
                  className="rounded-full p-1 text-ink-4 hover:text-ink text-[16px] leading-none"
                  aria-label={locale === "en" ? "Close preview" : "关闭"}
                >
                  ✕
                </button>
              </div>
            </div>

            {error && <div className="mt-3"><FormMessage error>{error}</FormMessage></div>}
            {previewBusy ? (
              <div className="py-12 text-center text-[13px] text-ink-4">
                正在加载信源历史文章…
              </div>
            ) : previewError ? (
              <div className="py-8 text-center text-[13px] text-hot">
                {previewError}
              </div>
            ) : (
              <div className="mt-4 flex-1 overflow-y-auto pr-1 scrollbar-thin">
                <div className="flex items-center justify-between text-[11.5px] text-ink-3 pb-2 border-b border-line">
                  <span>最近收录的文章（前 10 篇）</span>
                  <span>已累计收录 {previewData?.source.articleCount ?? 0} 篇 · {previewData?.source.subscriberCount ?? 0} 人订阅</span>
                </div>

                {!previewData?.articles.length ? (
                  <div className="py-8 text-center text-[12px] text-ink-4">
                    该信源暂无历史文章收录
                  </div>
                ) : (
                  <div className="mt-3 space-y-3.5">
                    {previewData.articles.map(article => (
                      <article key={article.id} className="rounded-control bg-bg-sunk/30 p-3 hover:bg-bg-sunk/60 transition-colors">
                        <a
                          href={article.url}
                          target="_blank"
                          rel="noreferrer"
                          className="text-[13.5px] font-bold text-ink hover:text-accent leading-snug line-clamp-2"
                        >
                          {article.title} ↗
                        </a>
                        <div className="mt-1 flex items-center gap-2 text-[11px] text-ink-4">
                          {article.author && <span>{article.author}</span>}
                          {article.author && <span>·</span>}
                          <span>{article.publishedAt ? new Date(article.publishedAt).toLocaleDateString() : "未知日期"}</span>
                        </div>
                        {article.excerpt && (
                          <p className="mt-1.5 text-[12px] text-ink-2 line-clamp-2 leading-relaxed">
                            {article.excerpt}
                          </p>
                        )}
                      </article>
                    ))}
                  </div>
                )}
              </div>
            )}
          </div>
        </div>
      )}

      {/* 一键关注弹窗 */}
      {quickTarget && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4 backdrop-blur-xs">
          <form onSubmit={submitQuickSubscribe} className="card w-full max-w-[420px] p-5 sm:p-6">
            <h3 className="text-[16px] font-bold text-ink flex items-center gap-2">
              <span>关注信源</span>
              <span className="text-[12px] font-normal text-ink-4 truncate">({quickTarget.name})</span>
            </h3>

            {quickError && (
              <p className="mt-2 text-[12px] text-hot">{quickError}</p>
            )}

            <div className="mt-4 space-y-3.5">
              <label className="text-[12px] font-medium text-ink-2 block">
                {locale === "en" ? "My source name (optional)" : "我的备注名（选填）"}
                <input
                  className={`${fieldClass} mt-1`}
                  maxLength={200}
                  value={quickName}
                  onChange={e => setQuickName(e.target.value)}
                  placeholder={quickTarget.name}
                />
              </label>

              <label className="text-[12px] font-medium text-ink-2 block">
                刷新抓取频率
                <select
                  className={`${fieldClass} mt-1`}
                  value={quickInterval}
                  onChange={e => setQuickInterval(Number(e.target.value))}
                >
                  <option value={30}>每 30 分钟检查一次</option>
                  <option value={60}>每 1 小时检查一次（推荐）</option>
                  <option value={120}>每 2 小时检查一次</option>
                  <option value={360}>每 6 小时检查一次</option>
                  <option value={720}>每 12 小时检查一次</option>
                </select>
              </label>
            </div>

            <p className="mt-3 text-[11px] text-ink-4 leading-relaxed">
              提示：平台已同步该信源的历史数据，关注后立即生效，新文章将自动纳入你的关键词与模型筛选。
            </p>

            <div className="mt-5 flex justify-end gap-2.5">
              <button
                type="button"
                onClick={() => setQuickTarget(null)}
                className="rounded-full border border-line px-4 py-1.5 text-[12px] font-semibold text-ink-3 hover:border-accent transition-colors"
              >
                取消
              </button>
              <button
                type="submit"
                disabled={quickBusy}
                className="rounded-full bg-accent px-5 py-1.5 text-[12px] font-semibold text-accent-contrast hover:bg-accent-ink disabled:opacity-50 transition-colors"
              >
                {quickBusy ? "关注中…" : "确认关注"}
              </button>
            </div>
          </form>
        </div>
      )}

      {/* 取消关注确认弹窗 */}
      {unfollowTarget && (
        <div
          role="dialog"
          aria-modal="true"
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4 backdrop-blur-xs"
          onClick={(e) => {
            if (e.target === e.currentTarget && !unfollowBusy) {
              setUnfollowTarget(null);
            }
          }}
        >
          <div className="card w-full max-w-[400px] p-5 sm:p-6 shadow-xl">
            <div className="flex items-start gap-3.5">
              <div className="flex size-10 shrink-0 items-center justify-center rounded-full bg-rose-500/10 text-rose-600 dark:bg-rose-500/20 dark:text-rose-400">
                <IconRss size={20} />
              </div>
              <div className="min-w-0 flex-1">
                <h3 className="text-[16px] font-bold text-ink">
                  {locale === "en" ? "Unfollow Source" : "取消关注信源"}
                </h3>
                <p className="mt-1.5 text-[13px] leading-relaxed text-ink-3">
                  {locale === "en"
                    ? `Are you sure you want to unfollow "${unfollowTarget.name}"? Historical articles and personal results will be kept.`
                    : `确定要取消关注“${unfollowTarget.name}”吗？历史文章和个人结果会完整保留。`}
                </p>
              </div>
            </div>

            <div className="mt-6 flex justify-end gap-2.5">
              <button
                type="button"
                disabled={unfollowBusy}
                onClick={() => {
                  setUnfollowTarget(null);
                }}
                className="rounded-full border border-line px-4 py-1.5 text-[12.5px] font-semibold text-ink-3 hover:border-accent hover:text-ink transition-colors disabled:opacity-50"
              >
                {locale === "en" ? "Cancel" : "取消"}
              </button>
              <button
                type="button"
                disabled={unfollowBusy}
                onClick={() => {
                  void confirmUnfollow();
                }}
                className="rounded-full bg-rose-600 px-5 py-1.5 text-[12.5px] font-semibold text-white shadow-xs hover:bg-rose-700 transition-colors disabled:opacity-50"
              >
                {unfollowBusy
                  ? (locale === "en" ? "Unfollowing..." : "正在取消…")
                  : (locale === "en" ? "Confirm Unfollow" : "确定取消关注")}
              </button>
            </div>
          </div>
        </div>
      )}
    </main>
  );
}
