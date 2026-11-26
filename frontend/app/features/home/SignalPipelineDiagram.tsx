import {
  IconClock,
  IconFilter,
  IconFlame,
  IconGlobe,
  IconRss,
  IconSparkles,
  IconTag,
} from "../../components/icons";

interface SignalPipelineDiagramProps {
  activeStage: number;
  onSelectStage: (index: number) => void;
}

const SOURCE_NODES = [
  {
    key: "rss",
    label: "RSS / Atom",
  },
  {
    key: "web",
    label: "Web 列表",
  },
  {
    key: "social",
    label: "X / 公众号",
  },
  {
    key: "custom",
    label: "独立订阅源",
  },
] as const;

type ToneKind = "rule" | "clay" | "accent" | "hot" | "amber";

const TONE_CARD: Record<ToneKind, { active: string; idle: string }> = {
  rule: {
    active: "border-rule/60 bg-surface shadow-[0_14px_34px_-24px_rgba(14,116,144,.38)] ring-1 ring-rule/20",
    idle: "border-line bg-surface/85 hover:border-rule/45 hover:bg-surface",
  },
  clay: {
    active: "border-clay/60 bg-surface shadow-[0_14px_34px_-24px_rgba(194,89,63,.38)] ring-1 ring-clay/20",
    idle: "border-line bg-surface/85 hover:border-clay/45 hover:bg-surface",
  },
  accent: {
    active: "border-accent/60 bg-surface shadow-[0_14px_34px_-24px_rgba(36,99,78,.38)] ring-1 ring-accent/20",
    idle: "border-line bg-surface/85 hover:border-accent/45 hover:bg-surface",
  },
  hot: {
    active: "border-hot/55 bg-surface shadow-[0_14px_34px_-24px_rgba(194,65,12,.35)] ring-1 ring-hot/20",
    idle: "border-line bg-surface/85 hover:border-hot/40 hover:bg-surface",
  },
  amber: {
    active: "border-amber/55 bg-surface shadow-[0_14px_34px_-24px_rgba(180,83,9,.32)] ring-1 ring-amber/20",
    idle: "border-line bg-surface/85 hover:border-amber/40 hover:bg-surface",
  },
};

/** Render a crisp chevron pointing right along a responsive connector anchor. */
function RightArrow({ topClass, toneClass }: { topClass: string; toneClass: string }) {
  return (
    <span
      aria-hidden="true"
      className={`pointer-events-none absolute right-0 ${topClass} -translate-y-1/2 transition-colors duration-200 ${toneClass}`}
    >
      <svg width="7" height="10" viewBox="0 0 7 10" fill="none">
        <path
          d="M1.25 1.5L5.25 5L1.25 8.5"
          stroke="currentColor"
          strokeWidth="1.6"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </svg>
    </span>
  );
}

/** Render a crisp chevron pointing down for the vertical mobile flow. */
function DownArrow({ toneClass }: { toneClass: string }) {
  return (
    <div
      aria-hidden="true"
      className={`flex flex-col items-center py-1 transition-colors duration-200 ${toneClass}`}
    >
      <span className="h-3.5 w-px bg-current" />
      <svg width="10" height="6" viewBox="0 0 10 6" fill="none" className="-mt-0.5">
        <path
          d="M1.5 1.25L5 4.75L8.5 1.25"
          stroke="currentColor"
          strokeWidth="1.5"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </svg>
    </div>
  );
}

/** Render the simplified end-to-end pipeline architecture diagram for the landing page. */
export function SignalPipelineDiagram({ activeStage, onSelectStage }: SignalPipelineDiagramProps) {
  // Derive stage flags and tone classes so hovering a stage illuminates its adjacent flow.
  const stage0 = activeStage === 0;
  const stage1 = activeStage === 1;
  const stage2 = activeStage === 2;
  const stage3 = activeStage === 3;

  const boxStyle = (tone: ToneKind, selected: boolean) => {
    return selected ? TONE_CARD[tone].active : TONE_CARD[tone].idle;
  };

  const ruleLineColor = stage0 ? "text-rule" : "text-line-strong";
  const splitLineColor = stage0 || stage1 ? "text-rule" : "text-line-strong";
  const boundaryLineColor = stage1 || stage2 ? "text-clay" : "text-line-strong";
  const deepLineColor = stage2 || stage3 ? "text-accent" : "text-line-strong";
  const outputLineColor = stage3 ? "text-clay" : "text-line-strong";

  return (
    <div className="mt-7 rounded-[22px] border border-line bg-surface/90 p-4 shadow-[var(--shadow-card)] backdrop-blur-xs sm:p-5">
      {/* Desktop horizontal architecture diagram */}
      <div className="hidden lg:block">
        <div className="mb-3.5 grid grid-cols-[1fr_auto_1fr] items-center gap-3 text-[11px]">
          <div className="flex items-center justify-between rounded-full border border-rule/25 bg-rule-softer px-3.5 py-1.5">
            <span className="mono whitespace-nowrap font-semibold uppercase tracking-[.12em] text-rule">
              0 TOKEN · 平台共享采集与本地规则降噪
            </span>
            <span className="whitespace-nowrap text-ink-3">不消耗个人额度</span>
          </div>

          <span className="mono whitespace-nowrap text-[10px] font-medium uppercase tracking-[.14em] text-clay">
            隔离边界
          </span>

          <div className="flex items-center justify-between rounded-full border border-clay/25 bg-clay-softer px-3.5 py-1.5">
            <span className="mono whitespace-nowrap font-semibold uppercase tracking-[.12em] text-clay">
              BYOK · 个人大模型整理（账号严格隔离）
            </span>
            <span className="whitespace-nowrap text-ink-3">仅精选文章调用</span>
          </div>
        </div>

        <div className="grid grid-cols-[0.95fr_20px_1.05fr_20px_1.14fr_28px_1.22fr_18px_1.12fr_20px_1.02fr] items-stretch">
          {/* Column 1: Multi-source inputs (source_A..D) */}
          <div className="grid min-w-0 grid-rows-4 gap-2">
            {SOURCE_NODES.map((source) => (
              <button
                key={source.key}
                type="button"
                onClick={() => onSelectStage(0)}
                onMouseEnter={() => onSelectStage(0)}
                className={`flex min-w-0 items-center justify-between gap-1.5 rounded-tile border px-2.5 py-2 text-left transition-all duration-200 ${
                  stage0
                    ? "border-rule/50 bg-surface text-ink"
                    : "border-line-soft bg-bg-sunk/55 text-ink-2 hover:border-rule/35 hover:bg-surface"
                }`}
              >
                <span className="whitespace-nowrap text-[11.5px] font-medium">{source.label}</span>
                <IconRss size={12} className="shrink-0 text-rule" />
              </button>
            ))}
          </div>

          {/* Connector 1: 4 sources converge into shared deduplication pool */}
          <div className="relative min-w-0">
            <svg
              aria-hidden="true"
              viewBox="0 0 100 100"
              preserveAspectRatio="none"
              className="h-full w-full"
            >
              {[12.5, 37.5, 62.5, 87.5].map((y) => (
                <path
                  key={y}
                  d={`M 0 ${y} C 52 ${y}, 48 50, 96 50`}
                  fill="none"
                  stroke="currentColor"
                  strokeWidth={stage0 ? 2 : 1.4}
                  vectorEffect="non-scaling-stroke"
                  className={`transition-colors duration-200 ${ruleLineColor}`}
                />
              ))}
            </svg>
            <RightArrow topClass="top-1/2" toneClass={ruleLineColor} />
          </div>

          {/* Column 2: Step 01 shared deduplication & backfill cutoff */}
          <div className="flex min-w-0 items-center">
            <button
              type="button"
              onClick={() => onSelectStage(0)}
              onMouseEnter={() => onSelectStage(0)}
              className={`w-full rounded-card border p-3 text-left transition-all duration-200 ${boxStyle("rule", stage0)}`}
            >
              <div className="flex items-center justify-between gap-1.5">
                <span className="mono whitespace-nowrap text-[10px] font-semibold text-rule">STEP 01</span>
                <span className="shrink-0 whitespace-nowrap rounded-full bg-rule-soft px-2 py-0.5 text-[10px] font-medium text-rule">
                  共享一次
                </span>
              </div>
              <div className="mt-2 whitespace-nowrap text-[13px] font-semibold text-ink">去重汇总</div>
              <p className="mt-1 text-[11px] leading-relaxed text-ink-3">
                URL 规范化合并同篇报道，拦截老旧回填
              </p>
            </button>
          </div>

          {/* Connector 2: Splits into label-filter vs full-tracking branches */}
          <div className="relative min-w-0">
            <svg
              aria-hidden="true"
              viewBox="0 0 100 100"
              preserveAspectRatio="none"
              className="h-full w-full"
            >
              {[25, 75].map((y) => (
                <path
                  key={y}
                  d={`M 0 50 C 50 50, 50 ${y}, 96 ${y}`}
                  fill="none"
                  stroke="currentColor"
                  strokeWidth={stage0 || stage1 ? 2 : 1.4}
                  vectorEffect="non-scaling-stroke"
                  className={`transition-colors duration-200 ${splitLineColor}`}
                />
              ))}
            </svg>
            <RightArrow topClass="top-[25%]" toneClass={splitLineColor} />
            <RightArrow topClass="top-[75%]" toneClass={splitLineColor} />
          </div>

          {/* Column 3: Step 02 dual-path local routing (label filter vs full tracking) */}
          <div className="grid min-w-0 grid-rows-2 gap-3">
            <button
              type="button"
              onClick={() => onSelectStage(1)}
              onMouseEnter={() => onSelectStage(1)}
              className={`flex flex-col justify-center rounded-card border p-3 text-left transition-all duration-200 ${boxStyle("rule", stage1)}`}
            >
              <div className="flex items-center justify-between gap-1.5">
                <span className="inline-flex shrink-0 items-center gap-1 whitespace-nowrap text-[10px] font-semibold text-rule">
                  <IconTag size={11} />
                  默认过滤
                </span>
                <span className="mono shrink-0 whitespace-nowrap text-[10px] font-medium text-ok-ink">0 Token</span>
              </div>
              <div className="mt-1.5 whitespace-nowrap text-[13px] font-semibold text-ink">标签规则初筛</div>
              <p className="mt-0.5 text-[11px] leading-snug text-ink-3">
                关键词 · 公司实体 · 订阅标签
              </p>
            </button>

            <button
              type="button"
              onClick={() => onSelectStage(1)}
              onMouseEnter={() => onSelectStage(1)}
              className={`flex flex-col justify-center rounded-card border border-dashed p-3 text-left transition-all duration-200 ${
                stage1
                  ? "border-rule/60 bg-surface shadow-[var(--shadow-card)] ring-1 ring-rule/20"
                  : "border-line-strong bg-bg-sunk/40 hover:bg-surface"
              }`}
            >
              <div className="flex items-center justify-between gap-1.5">
                <span className="inline-flex shrink-0 items-center gap-1 whitespace-nowrap text-[10px] font-semibold text-ink-2">
                  <IconGlobe size={11} className="text-rule" />
                  全量跟踪
                </span>
                <span className="mono shrink-0 whitespace-nowrap text-[10px] text-ink-4">可选开关</span>
              </div>
              <div className="mt-1.5 whitespace-nowrap text-[13px] font-semibold text-ink">跳过标签过滤</div>
              <p className="mt-0.5 text-[11px] leading-snug text-ink-3">
                指定重要信源直接放行
              </p>
            </button>
          </div>

          {/* Connector 3: Dashed zero-token / BYOK boundary & merge into Batch Triage */}
          <div className="relative min-w-0">
            <div
              aria-hidden="true"
              className="pointer-events-none absolute -top-2 -bottom-2 left-1/2 -translate-x-1/2 border-l border-dashed border-clay/45"
            />
            <svg
              aria-hidden="true"
              viewBox="0 0 100 100"
              preserveAspectRatio="none"
              className="relative z-10 h-full w-full"
            >
              {[25, 75].map((y) => (
                <path
                  key={y}
                  d={`M 0 ${y} C 52 ${y}, 48 50, 96 50`}
                  fill="none"
                  stroke="currentColor"
                  strokeWidth={stage1 || stage2 ? 2 : 1.4}
                  vectorEffect="non-scaling-stroke"
                  className={`transition-colors duration-200 ${boundaryLineColor}`}
                />
              ))}
            </svg>
            <RightArrow topClass="top-1/2" toneClass={boundaryLineColor} />
          </div>

          {/* Column 4: Step 03 Batch Triage (triggered when > 5 candidates) */}
          <div className="flex min-w-0 items-center">
            <button
              type="button"
              onClick={() => onSelectStage(2)}
              onMouseEnter={() => onSelectStage(2)}
              className={`w-full rounded-card border p-3 text-left transition-all duration-200 ${boxStyle("clay", stage2)}`}
            >
              <div className="flex items-center justify-between gap-1.5">
                <span className="inline-flex shrink-0 items-center gap-1 whitespace-nowrap mono text-[10px] font-semibold text-clay">
                  <IconFilter size={11} />
                  STEP 03
                </span>
                <span className="shrink-0 whitespace-nowrap rounded-full bg-clay-soft px-2 py-0.5 text-[10px] font-medium text-clay">
                  省 ~80%
                </span>
              </div>
              <div className="mt-2 whitespace-nowrap text-[13px] font-semibold text-ink">Batch Triage 初筛</div>
              <p className="mt-1 text-[11px] leading-relaxed text-ink-3">
                大于 5 篇时 25 选 5，打包剔除水文与重复
              </p>
              <div className="mt-2 inline-flex whitespace-nowrap rounded-full border border-clay/20 bg-clay-softer px-2 py-0.5 text-[10px] text-clay">
                5 篇及以下直通精读
              </div>
            </button>
          </div>

          {/* Connector 4: Batch Triage -> Deep LLM */}
          <div className="relative min-w-0">
            <svg
              aria-hidden="true"
              viewBox="0 0 100 100"
              preserveAspectRatio="none"
              className="h-full w-full"
            >
              <path
                d="M 0 50 L 96 50"
                fill="none"
                stroke="currentColor"
                strokeWidth={stage2 || stage3 ? 2 : 1.4}
                vectorEffect="non-scaling-stroke"
                className={`transition-colors duration-200 ${deepLineColor}`}
              />
            </svg>
            <RightArrow topClass="top-1/2" toneClass={deepLineColor} />
          </div>

          {/* Column 5: Step 04 Personal LLM deep scoring & summarization */}
          <div className="flex min-w-0 items-center">
            <button
              type="button"
              onClick={() => onSelectStage(3)}
              onMouseEnter={() => onSelectStage(3)}
              className={`w-full rounded-card border p-3 text-left transition-all duration-200 ${boxStyle("accent", stage3)}`}
            >
              <div className="flex items-center justify-between gap-1.5">
                <span className="inline-flex shrink-0 items-center gap-1 whitespace-nowrap mono text-[10px] font-semibold text-accent">
                  <IconSparkles size={11} />
                  STEP 04
                </span>
                <span className="mono shrink-0 whitespace-nowrap text-[10px] text-ink-4">100次/日</span>
              </div>
              <div className="mt-2 whitespace-nowrap text-[13px] font-semibold text-ink">个人 LLM 精读</div>
              <p className="mt-1 text-[11px] leading-relaxed text-ink-3">
                生成 0-100 相关性评分、中文标题与摘要
              </p>
            </button>
          </div>

          {/* Connector 5: Personal LLM -> 48h Hot & 08:00 Daily */}
          <div className="relative min-w-0">
            <svg
              aria-hidden="true"
              viewBox="0 0 100 100"
              preserveAspectRatio="none"
              className="h-full w-full"
            >
              {[25, 75].map((y) => (
                <path
                  key={y}
                  d={`M 0 50 C 50 50, 50 ${y}, 96 ${y}`}
                  fill="none"
                  stroke="currentColor"
                  strokeWidth={stage3 ? 2 : 1.4}
                  vectorEffect="non-scaling-stroke"
                  className={`transition-colors duration-200 ${outputLineColor}`}
                />
              ))}
            </svg>
            <RightArrow topClass="top-[25%]" toneClass={outputLineColor} />
            <RightArrow topClass="top-[75%]" toneClass={outputLineColor} />
          </div>

          {/* Column 6: Final outputs */}
          <div className="grid min-w-0 grid-rows-2 gap-3">
            <button
              type="button"
              onClick={() => onSelectStage(3)}
              onMouseEnter={() => onSelectStage(3)}
              className={`flex flex-col justify-center rounded-card border p-3 text-left transition-all duration-200 ${boxStyle("hot", stage3)}`}
            >
              <div className="flex items-center justify-between gap-1.5">
                <IconFlame size={13} className="text-hot" />
                <span className="mono whitespace-nowrap rounded-full bg-hot-soft px-2 py-0.5 text-[10px] font-semibold text-hot">
                  TOP 10
                </span>
              </div>
              <div className="mt-1.5 whitespace-nowrap text-[13px] font-semibold text-ink">48h 热点榜</div>
              <p className="mt-0.5 text-[11px] text-ink-3">按分排序 · 只读零消耗</p>
            </button>

            <button
              type="button"
              onClick={() => onSelectStage(3)}
              onMouseEnter={() => onSelectStage(3)}
              className={`flex flex-col justify-center rounded-card border p-3 text-left transition-all duration-200 ${boxStyle("amber", stage3)}`}
            >
              <div className="flex items-center justify-between gap-1.5">
                <IconClock size={13} className="text-amber-ink" />
                <span className="mono whitespace-nowrap rounded-full bg-amber-soft px-2 py-0.5 text-[10px] font-semibold text-amber-ink">
                  08:00
                </span>
              </div>
              <div className="mt-1.5 whitespace-nowrap text-[13px] font-semibold text-ink">每日简报</div>
              <p className="mt-0.5 text-[11px] text-ink-3">汇总此前 24h 脉络</p>
            </button>
          </div>
        </div>
      </div>

      {/* Mobile & tablet compact vertical architecture diagram */}
      <div className="space-y-2 lg:hidden">
        <div className="flex items-center justify-between rounded-full border border-rule/25 bg-rule-softer px-3 py-1 text-[10.5px]">
          <span className="mono font-semibold uppercase tracking-[.1em] text-rule">
            0 TOKEN · 共享采集与规则降噪
          </span>
        </div>

        <div className="grid grid-cols-2 gap-2">
          {SOURCE_NODES.map((source) => (
            <button
              key={source.key}
              type="button"
              onClick={() => onSelectStage(0)}
              className="flex items-center justify-between rounded-tile border border-line-soft bg-bg-sunk/55 px-3 py-2 text-left text-[12px] font-medium text-ink-2"
            >
              <span>{source.label}</span>
              <IconRss size={12} className="text-rule" />
            </button>
          ))}
        </div>

        <DownArrow toneClass={ruleLineColor} />

        <button
          type="button"
          onClick={() => onSelectStage(0)}
          className={`w-full rounded-card border p-3.5 text-left transition-colors ${boxStyle("rule", stage0)}`}
        >
          <div className="flex items-center justify-between">
            <span className="mono text-[10.5px] font-semibold text-rule">STEP 01 · 去重汇总</span>
            <span className="text-[10.5px] text-ink-4">URL 规范化 · 拦截旧回填</span>
          </div>
          <p className="mt-1 text-[12px] text-ink-3">多信源同篇文章合并入库，不重复触发处理。</p>
        </button>

        <DownArrow toneClass={splitLineColor} />

        <div className="grid grid-cols-2 gap-2.5">
          <button
            type="button"
            onClick={() => onSelectStage(1)}
            className={`rounded-card border p-3 text-left transition-colors ${boxStyle("rule", stage1)}`}
          >
            <div className="mono text-[10px] font-semibold text-rule">默认 · 0 TOKEN</div>
            <div className="mt-1 text-[13px] font-semibold text-ink">标签规则初筛</div>
            <p className="mt-0.5 text-[11px] text-ink-3">关键词 / 实体 / 标签匹配</p>
          </button>

          <button
            type="button"
            onClick={() => onSelectStage(1)}
            className="rounded-card border border-dashed border-line-strong bg-bg-sunk/40 p-3 text-left"
          >
            <div className="mono text-[10px] font-semibold text-ink-3">可选 · 全量跟踪</div>
            <div className="mt-1 text-[13px] font-semibold text-ink">跳过标签过滤</div>
            <p className="mt-0.5 text-[11px] text-ink-3">重要源直接放行</p>
          </button>
        </div>

        <div className="relative py-2">
          <div className="border-t border-dashed border-clay/45" />
          <span className="mono absolute left-1/2 top-1/2 -translate-x-1/2 -translate-y-1/2 rounded-full border border-clay/30 bg-surface px-2.5 py-0.5 text-[10px] font-semibold text-clay">
            BYOK 个人模型边界
          </span>
        </div>

        <button
          type="button"
          onClick={() => onSelectStage(2)}
          className={`w-full rounded-card border p-3.5 text-left transition-colors ${boxStyle("clay", stage2)}`}
        >
          <div className="flex items-center justify-between">
            <span className="mono text-[10.5px] font-semibold text-clay">STEP 03 · Batch Triage</span>
            <span className="rounded-full bg-clay-soft px-2 py-0.5 text-[10px] font-medium text-clay">
              省 ~80% Token
            </span>
          </div>
          <p className="mt-1 text-[12px] text-ink-3">大于 5 篇时 25 选 5，剔除水文重复；5 篇及以下直通精读。</p>
        </button>

        <DownArrow toneClass={deepLineColor} />

        <button
          type="button"
          onClick={() => onSelectStage(3)}
          className={`w-full rounded-card border p-3.5 text-left transition-colors ${boxStyle("accent", stage3)}`}
        >
          <div className="flex items-center justify-between">
            <span className="mono text-[10.5px] font-semibold text-accent">STEP 04 · 个人 LLM 精读</span>
            <span className="text-[10px] text-ink-4">账号严格隔离</span>
          </div>
          <p className="mt-1 text-[12px] text-ink-3">生成 0-100 相关性打分、中文标题与摘要，沉淀到 48h Top 10 热点榜与每日 08:00 简报。</p>
        </button>
      </div>
    </div>
  );
}
