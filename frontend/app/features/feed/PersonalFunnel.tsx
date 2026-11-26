import { useState } from "react";
import { Link } from "react-router";
import { IconBolt } from "../../components/icons";

export interface FunnelView {
  totalScanned: number;
  passedCount: number;
  filteredCount: number;
  filteredItems: Array<{
    articleId: string;
    title: string;
    source: string;
    url: string;
    publishedAt?: string | null;
    createdAt: string;
  }>;
}

/** Show today's scan and filter statistics with the expandable intercepted article list. */
export function PersonalFunnel({ funnel, isEn }: { funnel: FunnelView; isEn: boolean }) {
  const [showFiltered, setShowFiltered] = useState(false);

  return (
    <section className="mb-6 rounded-card border border-line bg-surface p-4 sm:p-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-2.5">
          <span className="flex size-7 shrink-0 items-center justify-center rounded-full bg-rule-soft text-rule">
            <IconBolt size={14} />
          </span>
          <div>
            <h2 className="text-[13.5px] font-semibold text-ink">
              {isEn ? "Today's Stream & Filter Funnel" : "今日信息流与过滤动态"}
            </h2>
            <p className="mt-0.5 text-[12px] text-ink-3">
              {isEn
                ? `Scanned ${funnel.totalScanned} articles · ${funnel.passedCount} evaluated · ${funnel.filteredCount} pending or filtered`
                : `已扫描 ${funnel.totalScanned} 篇资讯 · ${funnel.passedCount} 篇精读入库 · ${funnel.filteredCount} 篇待加工或已拦截`}
            </p>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-3 text-[12px]">
          {funnel.filteredCount > 0 && (
            <button
              type="button"
              aria-expanded={showFiltered}
              aria-controls="filtered-articles"
              onClick={() => setShowFiltered(!showFiltered)}
              className="font-medium text-accent hover:underline"
            >
              {showFiltered
                ? (isEn ? "Hide List ▲" : "收起清单 ▲")
                : (isEn ? `View ${funnel.filteredCount} Pending / Filtered ▼` : `查看 ${funnel.filteredCount} 篇待加工/已拦截清单 ▼`)}
            </button>
          )}
          <Link to="/interests" className="text-ink-4 hover:text-accent">
            {isEn ? "Manage tags" : "调整标签"}
          </Link>
          <Link to="/sources" className="text-ink-4 hover:text-accent">
            {isEn ? "Sources" : "管理信源"}
          </Link>
        </div>
      </div>
      <div id="filtered-articles" hidden={!showFiltered}>
        <div className="mt-4 border-t border-line pt-3">
          <p className="mb-2 text-[11px] text-ink-4">
            {isEn
              ? "Articles without a personal model result (including those filtered out or still processing):"
              : "以下文章尚无个人模型结果，包含已过滤或仍在等待加工的文章："}
          </p>
          <ul className="divide-y divide-line-soft">
            {funnel.filteredItems.map((item) => (
              <li key={item.articleId} className="flex items-start justify-between gap-4 py-2">
                <div className="min-w-0 flex-1">
                  <a href={item.url} target="_blank" rel="noopener noreferrer" className="line-clamp-1 text-[12.5px] font-medium text-ink hover:text-accent hover:underline">
                    {item.title} ↗
                  </a>
                  <div className="mt-0.5 text-[11px] text-ink-4">
                    <span>{item.source} · </span>
                    <time>
                      {new Date(item.publishedAt ?? item.createdAt).toLocaleTimeString(isEn ? "en-US" : "zh-CN", {
                        timeZone: "Asia/Shanghai",
                        hour: "2-digit",
                        minute: "2-digit",
                      })}
                    </time>
                  </div>
                </div>
              </li>
            ))}
          </ul>
        </div>
      </div>
    </section>
  );
}
