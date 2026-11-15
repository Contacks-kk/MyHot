import { useState } from "react";
import { Form, Link, useSearchParams } from "react-router";
import { SITE } from "@aihot/site";
import { CATEGORY_LABELS } from "@aihot/contracts/taxonomy";
import type { Route } from "./+types/labels";
import type {
  AdminLabelsOverview,
  AdminLabelHotspotsResult,
  AdminStableLabel,
  AdminCandidateLabel,
} from "@aihot/contracts/admin";
import { adminGet } from "../../lib/admin.server";
import { bj, num } from "../../features/admin/format";
import {
  AdminPage,
  Card,
  DataTable,
  Badge,
  Input,
  Select,
  Pager,
  Stat,
} from "../../features/admin/ui";

/** loader retrieves label statistics and filtered hotspot publications. */
export async function loader({ request }: Route.LoaderArgs) {
  const url = new URL(request.url);

  // 并行请求标签总览统计与热点文章列表
  const [overview, hotspots] = await Promise.all([
    adminGet<AdminLabelsOverview>(request, "/api/admin/labels"),
    adminGet<AdminLabelHotspotsResult>(
      request,
      `/api/admin/labels/hotspots${url.search}`
    ),
  ]);

  return {
    overview,
    hotspots,
  };
}

export const meta: Route.MetaFunction = () => [
  { title: `标签 · ${SITE.name} 后台` },
];

/** AdminLabelsPage renders stable labels, promotion candidates, and ranked hotspots. */
export default function AdminLabelsPage({ loaderData }: Route.ComponentProps) {
  const { overview, hotspots } = loaderData;
  const [searchParams, setSearchParams] = useSearchParams();

  const [activeTab, setActiveTab] = useState<"stable" | "candidate">("stable");
  const [stableTypeFilter, setStableTypeFilter] = useState<string>("all");
  const [filterQuery, setFilterQuery] = useState("");

  const currentLabel = searchParams.get("label") ?? "";
  const currentQ = searchParams.get("q") ?? "";
  const currentSort = searchParams.get("sort") ?? "score";
  const currentPage = Number(searchParams.get("page") ?? "1");

  /** Select a label to inspect its associated hotspot articles. */
  function selectLabel(name: string) {
    const next = new URLSearchParams(searchParams);
    if (name) {
      next.set("label", name);
    } else {
      next.delete("label");
    }
    next.set("page", "1");
    setSearchParams(next);
  }

  /** Filter stable labels by type and search query. */
  const filteredStable = overview.stable_labels.filter((label: AdminStableLabel) => {
    if (stableTypeFilter !== "all" && label.type !== stableTypeFilter) {
      return false;
    }
    if (filterQuery.trim()) {
      return label.name.toLowerCase().includes(filterQuery.trim().toLowerCase());
    }
    return true;
  });

  /** Filter candidate labels by search query. */
  const filteredCandidate = overview.candidate_labels.filter(
    (label: AdminCandidateLabel) => {
      if (!filterQuery.trim()) {
        return true;
      }
      const q = filterQuery.trim().toLowerCase();
      return (
        label.name.toLowerCase().includes(q) ||
        label.mapped_to.toLowerCase().includes(q)
      );
    }
  );

  return (
    <AdminPage
      title="标签管理与热点追踪"
      subtitle="查看系统稳定核心标签、观察评估待晋升候选词，并检索关联热点文章与打分排名。"
    >
      {/* 顶部总体统计指标 */}
      <div className="mb-6 grid grid-cols-1 gap-4 sm:grid-cols-3">
        <Stat
          label="稳定使用的核心标签"
          value={overview.totals.stable_count}
          hint="包含已标准化的主题、分类及重点实体词"
          tone="ok"
        />
        <Stat
          label="正在评估晋升的标签"
          value={overview.totals.candidate_count}
          hint="包含已映射近义词与近期新兴高频标签"
          tone="warn"
        />
        <Stat
          label="热点文章打标引用数"
          value={overview.totals.total_tagged}
          hint="全站公开热点收录关联的历史标签频次总和"
        />
      </div>

      {/* 标签管理核心区：稳定标签 vs 待晋升标签 */}
      <Card
        title={
          <div className="flex items-center gap-3">
            <button
              type="button"
              onClick={() => setActiveTab("stable")}
              className={`pb-1 text-[14px] font-semibold transition-colors ${
                activeTab === "stable"
                  ? "border-b-2 border-accent text-ink"
                  : "text-ink-3 hover:text-ink"
              }`}
            >
              稳定核心标签 ({overview.stable_labels.length})
            </button>
            <button
              type="button"
              onClick={() => setActiveTab("candidate")}
              className={`pb-1 text-[14px] font-semibold transition-colors ${
                activeTab === "candidate"
                  ? "border-b-2 border-accent text-ink"
                  : "text-ink-3 hover:text-ink"
              }`}
            >
              正在决定是否晋升的标签 ({overview.candidate_labels.length})
            </button>
          </div>
        }
        right={
          <div className="flex items-center gap-2">
            {activeTab === "stable" && (
              <div className="flex items-center rounded-control border border-line bg-surface p-0.5 text-[11px]">
                <button
                  type="button"
                  onClick={() => setStableTypeFilter("all")}
                  className={`rounded px-2 py-0.5 transition-colors ${
                    stableTypeFilter === "all"
                      ? "bg-accent-soft font-semibold text-accent"
                      : "text-ink-3 hover:text-ink"
                  }`}
                >
                  全部
                </button>
                <button
                  type="button"
                  onClick={() => setStableTypeFilter("topic")}
                  className={`rounded px-2 py-0.5 transition-colors ${
                    stableTypeFilter === "topic"
                      ? "bg-accent-soft font-semibold text-accent"
                      : "text-ink-3 hover:text-ink"
                  }`}
                >
                  主题
                </button>
                <button
                  type="button"
                  onClick={() => setStableTypeFilter("category")}
                  className={`rounded px-2 py-0.5 transition-colors ${
                    stableTypeFilter === "category"
                      ? "bg-accent-soft font-semibold text-accent"
                      : "text-ink-3 hover:text-ink"
                  }`}
                >
                  分类
                </button>
                <button
                  type="button"
                  onClick={() => setStableTypeFilter("entity")}
                  className={`rounded px-2 py-0.5 transition-colors ${
                    stableTypeFilter === "entity"
                      ? "bg-accent-soft font-semibold text-accent"
                      : "text-ink-3 hover:text-ink"
                  }`}
                >
                  实体
                </button>
              </div>
            )}
            <Input
              value={filterQuery}
              onChange={(e) => setFilterQuery(e.target.value)}
              placeholder="过滤当前标签列表..."
              className="w-44 text-[12px]"
            />
          </div>
        }
        pad={false}
      >
        {activeTab === "stable" ? (
          <DataTable
            rows={filteredStable}
            rowKey={(r: AdminStableLabel) => r.name}
            empty="没有符合条件的稳定标签"
            columns={[
              {
                key: "name",
                label: "标签名称",
                render: (r: AdminStableLabel) => {
                  const isSelected = currentLabel === r.name;
                  return (
                    <div className="flex items-center gap-2">
                      <span
                        className={`font-mono text-[13px] font-semibold ${
                          isSelected ? "text-accent" : "text-ink"
                        }`}
                      >
                        {r.name}
                      </span>
                      {isSelected && (
                        <span className="rounded-full bg-accent-soft px-1.5 py-0.2 text-[9px] font-bold text-accent">
                          当前选中
                        </span>
                      )}
                    </div>
                  );
                },
              },
              {
                key: "type",
                label: "标签类别",
                render: (r: AdminStableLabel) => {
                  if (r.type === "topic") {
                    return <Badge tone="accent">核心主题</Badge>;
                  }
                  if (r.type === "category") {
                    return <Badge tone="ok">分类标签</Badge>;
                  }
                  return <Badge tone="muted">机构实体</Badge>;
                },
              },
              {
                key: "count",
                label: "关联热点",
                render: (r: AdminStableLabel) => (
                  <span className="num text-[12.5px] font-medium text-ink">
                    {num(r.article_count)} 篇
                  </span>
                ),
              },
              {
                key: "selected",
                label: "精选头条",
                render: (r: AdminStableLabel) => (
                  <span className="num text-[12px] text-ink-3">
                    {r.selected_count > 0 ? (
                      <span className="font-semibold text-hot">
                        {num(r.selected_count)} 篇
                      </span>
                    ) : (
                      "-"
                    )}
                  </span>
                ),
              },
              {
                key: "score",
                label: "打分情况",
                render: (r: AdminStableLabel) => {
                  if (r.article_count === 0) {
                    return <span className="text-ink-4">-</span>;
                  }
                  return (
                    <span className="num text-[12px] text-ink-2">
                      最高: <strong className="text-ink">{r.max_score}</strong> · 均分:{" "}
                      {r.avg_score}
                    </span>
                  );
                },
              },
              {
                key: "latest",
                label: "最新收录时间",
                render: (r: AdminStableLabel) => (
                  <span className="num whitespace-nowrap text-[11.5px] text-ink-3">
                    {r.latest_at ? bj(r.latest_at, false) : "暂无"}
                  </span>
                ),
              },
              {
                key: "action",
                label: "操作",
                render: (r: AdminStableLabel) => (
                  <button
                    type="button"
                    onClick={() => selectLabel(r.name)}
                    className="inline-flex items-center gap-1 text-[12px] font-medium text-accent hover:underline"
                  >
                    <span>查看关联热点</span>
                    <span>↓</span>
                  </button>
                ),
              },
            ]}
          />
        ) : (
          <DataTable
            rows={filteredCandidate}
            rowKey={(r: AdminCandidateLabel) => r.name}
            empty="暂无待晋升候选标签"
            columns={[
              {
                key: "name",
                label: "候选标签名称",
                render: (r: AdminCandidateLabel) => {
                  const isSelected = currentLabel === r.name;
                  return (
                    <div className="flex items-center gap-2">
                      <span
                        className={`font-mono text-[13px] font-semibold ${
                          isSelected ? "text-accent" : "text-ink"
                        }`}
                      >
                        {r.name}
                      </span>
                      {isSelected && (
                        <span className="rounded-full bg-accent-soft px-1.5 py-0.2 text-[9px] font-bold text-accent">
                          当前选中
                        </span>
                      )}
                    </div>
                  );
                },
              },
              {
                key: "mapped",
                label: "规范化目标 / 归属",
                render: (r: AdminCandidateLabel) => {
                  if (r.mapped_to) {
                    return (
                      <span className="inline-flex items-center gap-1.5 text-[12px] text-ink-2">
                        <span className="text-ink-4">→ 映射至</span>
                        <strong className="font-medium text-accent">{r.mapped_to}</strong>
                      </span>
                    );
                  }
                  return <span className="text-[12px] text-ink-4">尚无映射（文章自发提炼）</span>;
                },
              },
              {
                key: "status",
                label: "当前状态",
                render: (r: AdminCandidateLabel) => {
                  if (r.status === "synonym") {
                    return <Badge tone="accent">近义词合并中</Badge>;
                  }
                  return <Badge tone="warn">新兴待决断晋升</Badge>;
                },
              },
              {
                key: "count",
                label: "出现频次",
                render: (r: AdminCandidateLabel) => (
                  <span className="num text-[12.5px] font-medium text-ink">
                    {num(r.article_count)} 次
                  </span>
                ),
              },
              {
                key: "score",
                label: "热点打分",
                render: (r: AdminCandidateLabel) => {
                  if (r.article_count === 0) {
                    return <span className="text-ink-4">-</span>;
                  }
                  return (
                    <span className="num text-[12px] text-ink-2">
                      最高: <strong className="text-ink">{r.max_score}</strong> · 均分:{" "}
                      {r.avg_score}
                    </span>
                  );
                },
              },
              {
                key: "action",
                label: "操作",
                render: (r: AdminCandidateLabel) => (
                  <button
                    type="button"
                    onClick={() => selectLabel(r.name)}
                    className="inline-flex items-center gap-1 text-[12px] font-medium text-accent hover:underline"
                  >
                    <span>查看关联热点</span>
                    <span>↓</span>
                  </button>
                ),
              },
            ]}
          />
        )}
      </Card>

      {/* 关联热点探索与模糊查询打分排名区 */}
      <section className="mt-8">
        <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 className="text-[16px] font-bold text-ink flex items-center gap-2">
              <span>🔥 关联热点文章探索</span>
              {currentLabel && (
                <span className="inline-flex items-center gap-1 rounded-full bg-accent-soft px-2.5 py-0.5 text-[11px] font-semibold text-accent">
                  <span>标签：{currentLabel}</span>
                  <button
                    type="button"
                    onClick={() => selectLabel("")}
                    className="ml-0.5 text-accent hover:text-accent-ink"
                    title="清除标签筛选"
                  >
                    ✕
                  </button>
                </span>
              )}
            </h2>
            <p className="mt-1 text-[12px] text-ink-3">
              {currentLabel
                ? `检索与标签【${currentLabel}】相关的热点文章，支持输入关键字模糊过滤与按打分排序。`
                : "当前展示全量热点文章。点击上方任意标签或使用下方搜索框可聚焦特定领域热点。"}
            </p>
          </div>

          <span className="num text-[12px] text-ink-4">
            共找到 <strong>{hotspots.total}</strong> 篇关联热点
          </span>
        </div>

        {/* 检索与排序表单 */}
        <Form method="get" className="mb-4 flex flex-wrap items-center gap-3">
          {currentLabel && <input type="hidden" name="label" value={currentLabel} />}

          <div className="flex-1 min-w-[220px]">
            <Input
              name="q"
              defaultValue={currentQ}
              placeholder="模糊搜索标题、摘要导读或关联标签..."
              aria-label="按关键词模糊查询"
            />
          </div>

          <div className="w-48">
            <Select name="sort" defaultValue={currentSort} aria-label="热点排序方式">
              <option value="score">最高打分排名 (高 → 低)</option>
              <option value="recent">发布时间 (新 → 旧)</option>
              <option value="score_asc">最低打分 (低 → 高)</option>
            </Select>
          </div>

          <button
            type="submit"
            className="h-9 rounded-control bg-accent px-4 text-[12.5px] font-semibold text-accent-contrast hover:bg-accent-ink transition-colors"
          >
            查询热点
          </button>

          {(currentQ || currentLabel) && (
            <Link
              to="/admin/labels"
              className="text-[12px] text-ink-4 hover:text-accent"
            >
              重置筛选
            </Link>
          )}
        </Form>

        {/* 热点文章列表展示 */}
        <Card pad={false}>
          <DataTable
            rows={hotspots.rows}
            rowKey={(r) => r.article_id}
            empty="未找到符合条件的关联热点文章"
            columns={[
              {
                key: "score",
                label: "打分",
                render: (r) => {
                  if (r.score == null) {
                    return <span className="text-ink-4">-</span>;
                  }
                  const isHigh = r.score >= 80;
                  return (
                    <span
                      className={`num rounded px-2 py-0.5 text-[12px] font-bold ${
                        isHigh
                          ? "bg-hot/15 text-hot"
                          : "bg-surface-2 text-ink-2 border border-line/60"
                      }`}
                    >
                      {r.score.toFixed(1)}
                    </span>
                  );
                },
              },
              {
                key: "title",
                label: "热点标题 / 导读",
                render: (r) => (
                  <div className="space-y-1 py-1 max-w-xl">
                    <div className="flex flex-wrap items-center gap-2">
                      {r.selected && <Badge tone="accent">精选头条</Badge>}
                      {r.category && (
                        <span className="rounded bg-surface-2 px-1.5 py-0.5 text-[10.5px] font-medium text-ink-3">
                          {CATEGORY_LABELS[r.category as keyof typeof CATEGORY_LABELS] ??
                            r.category}
                        </span>
                      )}
                      <Link
                        to={`/admin/content/${r.article_id}`}
                        className="text-[13.5px] font-semibold text-ink hover:text-accent hover:underline line-clamp-1"
                      >
                        {r.title}
                      </Link>
                      <a
                        href={r.url}
                        target="_blank"
                        rel="noreferrer"
                        className="text-[11px] text-ink-4 hover:text-accent"
                        title="在新标签页中打开原文"
                      >
                        ↗
                      </a>
                    </div>

                    {r.summary && (
                      <p className="line-clamp-2 text-[12px] text-ink-3 leading-relaxed">
                        {r.summary}
                      </p>
                    )}

                    {r.tags && r.tags.length > 0 && (
                      <div className="flex flex-wrap items-center gap-1.5 pt-0.5">
                        <span className="text-[10px] text-ink-4">🏷️</span>
                        {r.tags.map((t) => {
                          const isCurrent = t === currentLabel;
                          return (
                            <button
                              key={t}
                              type="button"
                              onClick={() => selectLabel(t)}
                              className={`rounded px-1.5 py-0.2 text-[10.5px] transition-colors ${
                                isCurrent
                                  ? "bg-accent text-accent-contrast font-semibold"
                                  : "bg-surface-2 text-ink-3 hover:text-accent hover:bg-accent-soft"
                              }`}
                            >
                              {t}
                            </button>
                          );
                        })}
                      </div>
                    )}
                  </div>
                ),
              },
              {
                key: "source",
                label: "信源 / 渠道",
                render: (r) => (
                  <span className="text-[12px] text-ink-3 whitespace-nowrap">
                    {r.source}
                  </span>
                ),
              },
              {
                key: "published",
                label: "发布时间",
                render: (r) => (
                  <span className="num whitespace-nowrap text-[11.5px] text-ink-4">
                    {r.published_at ? bj(r.published_at, false) : bj(r.discovered_at, false)}
                  </span>
                ),
              },
            ]}
          />
        </Card>

        <Pager
          page={currentPage}
          hasMore={currentPage * 20 < hotspots.total}
        />
      </section>
    </AdminPage>
  );
}
