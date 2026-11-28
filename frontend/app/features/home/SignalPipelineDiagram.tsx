import { useId, useState, type KeyboardEvent } from "react";
import { IconArrowLeft, IconFilter, IconFlame, IconRss, IconSparkles } from "../../components/icons";
import {
  MACRO_POOL_NODES,
  MACRO_QUEUE_SOURCES,
  MACRO_USER_LANES,
  type PipelineLevel,
} from "./pipelineData";

interface SignalPipelineDiagramProps {
  level: PipelineLevel;
  selectedUserId: string;
  activeMacroStage: number;
  activeDetailStage: number;
  onDrillDown: (userId: string) => void;
  onBackToMacro: () => void;
  onSelectMacroStage: (index: number) => void;
  onSelectDetailStage: (index: number) => void;
}

const L2_INPUT_SOURCES = [
  {
    name: "RSS / Atom",
    y: 82,
  },
  {
    name: "Web 列表",
    y: 162,
  },
  {
    name: "X / 公众号",
    y: 242,
  },
  {
    name: "独立订阅源",
    y: 322,
  },
] as const;

/** handleKeyTrigger activates an SVG group node on Enter or Space for keyboard accessibility. */
function handleKeyTrigger(event: KeyboardEvent<SVGGElement>, action: () => void) {
  if (event.key === "Enter" || event.key === " ") {
    event.preventDefault();
    action();
  }
}

/** SignalPipelineDiagram renders the zoomable dual-layer SVG architecture (macro overview <-> 4-stage funnel). */
export function SignalPipelineDiagram({
  level,
  selectedUserId,
  activeMacroStage,
  activeDetailStage,
  onDrillDown,
  onBackToMacro,
  onSelectMacroStage,
  onSelectDetailStage,
}: SignalPipelineDiagramProps) {
  const uid = useId().replace(/:/g, "");
  const [hoveredUserId, setHoveredUserId] = useState<string | null>(null);

  const selectedUser = MACRO_USER_LANES.find((user) => user.id === selectedUserId) ?? MACRO_USER_LANES[0]!;

  if (level === "macro") {
    const queueActive = activeMacroStage === 0;
    const poolActive = activeMacroStage === 1;
    const processActive = activeMacroStage === 2;
    const workspaceActive = activeMacroStage === 3;

    const activeUserFilter = hoveredUserId ? MACRO_USER_LANES.find((u) => u.id === hoveredUserId) ?? null : null;

    return (
      <div
        key="macro-level"
        className="landing-pipeline-zoom mt-7 rounded-[22px] border border-line bg-surface/90 p-4 shadow-[var(--shadow-card)] backdrop-blur-xs sm:p-5"
      >
        {/* Desktop unified SVG macro graph */}
        <div className="hidden lg:block">
          <div className="mb-3 grid grid-cols-[190px_1.08fr_auto_1.35fr] items-center gap-2.5 text-[11px]">
            <div className="mono rounded-full border border-rule/30 bg-rule-softer px-2 py-1 text-center font-semibold uppercase tracking-[.12em] text-rule">
              WAIT QUEUE
            </div>
            <div className="mono flex items-center justify-between rounded-full border border-line bg-bg-sunk/75 px-3 py-1 font-semibold text-ink-2">
              <span>0 TOKEN · 共享抓取与去重落库</span>
              <span className="text-ink-4">同源存 1 份</span>
            </div>
            <span className="mono text-[10px] font-semibold uppercase tracking-[.14em] text-clay">
              隔离边界
            </span>
            <div className="mono flex items-center justify-between rounded-full border border-clay/30 bg-clay-softer px-3 py-1 font-semibold text-clay">
              <span>按订阅私有加工 → 工作区 · 点击【加工】放大</span>
              <span aria-hidden="true">⤢</span>
            </div>
          </div>

          <svg viewBox="0 0 1060 458" className="h-auto w-full select-none overflow-visible">
            <defs>
              <marker
                id={`l1-arrow-rule-${uid}`}
                viewBox="0 0 10 10"
                refX="8"
                refY="5"
                markerWidth="6"
                markerHeight="6"
                orient="auto-start-reverse"
              >
                <path d="M 1 1.5 L 8.5 5 L 1 8.5 z" fill="var(--rule)" />
              </marker>
              {MACRO_USER_LANES.map((lane) => (
                <marker
                  key={lane.id}
                  id={`l1-arrow-${lane.arrowSuffix}-${uid}`}
                  viewBox="0 0 10 10"
                  refX="8"
                  refY="5"
                  markerWidth="6"
                  markerHeight="6"
                  orient="auto-start-reverse"
                >
                  <path d="M 1 1.5 L 8.5 5 L 1 8.5 z" fill={lane.strokeVar} />
                </marker>
              ))}
              <marker
                id={`l1-arrow-accent-${uid}`}
                viewBox="0 0 10 10"
                refX="8"
                refY="5"
                markerWidth="6"
                markerHeight="6"
                orient="auto-start-reverse"
              >
                <path d="M 1 1.5 L 8.5 5 L 1 8.5 z" fill="var(--accent)" />
              </marker>
            </defs>

            {/* Vertical Divider 1: Queue -> Shared Storage */}
            <line x1="216" y1="16" x2="216" y2="442" stroke="var(--line-strong)" strokeWidth="1.5" />

            {/* Vertical Divider 2: Clean Isolation Boundary channel (between pool fan-out and personal processing) */}
            <line
              x1="598"
              y1="12"
              x2="598"
              y2="446"
              stroke="var(--clay)"
              strokeWidth="1.4"
              strokeDasharray="6 5"
              opacity="0.72"
            />

            {/* WaitQueue outer box */}
            <g
              role="button"
              tabIndex={0}
              aria-label="第一步：活跃信源调度队列"
              className="cursor-pointer focus:outline-none"
              onClick={() => onSelectMacroStage(0)}
              onMouseEnter={() => onSelectMacroStage(0)}
              onKeyDown={(event) => handleKeyTrigger(event, () => onSelectMacroStage(0))}
            >
              <rect
                x="14"
                y="16"
                width="176"
                height="426"
                rx="18"
                fill="var(--surface)"
                stroke={queueActive ? "var(--rule)" : "var(--line)"}
                strokeWidth={queueActive ? "2" : "1.4"}
              />
              <text
                x="102"
                y="34"
                textAnchor="middle"
                fill="var(--ink-4)"
                fontSize="10.5"
                fontWeight="600"
                letterSpacing="0.8"
              >
                活跃信源队列 (并发≤2)
              </text>

              {MACRO_QUEUE_SOURCES.map((source) => {
                const lit = !activeUserFilter || activeUserFilter.subs.includes(source.id);
                return (
                  <g key={source.id} style={{ opacity: lit ? 1 : 0.22, transition: "opacity 180ms ease" }}>
                    <rect
                      x="26"
                      y={source.y - 18}
                      width="150"
                      height="36"
                      rx="18"
                      fill="var(--bg)"
                      stroke="var(--rule)"
                      strokeWidth="1.2"
                    />
                    <text x="38" y={source.y + 4} textAnchor="start" fill="var(--ink)" fontSize="11.5" fontWeight="600">
                      {source.name}
                    </text>
                    <text x="164" y={source.y + 3.5} textAnchor="end" fill="var(--rule)" fontSize="9.5" fontWeight="700">
                      {source.tag}
                    </text>
                  </g>
                );
              })}
            </g>

            {/* Queue -> Shared Pool dashed Bezier edges + signal dots */}
            {MACRO_QUEUE_SOURCES.map((source, index) => {
              const targetPool = MACRO_POOL_NODES.find((node) => node.id === source.id);
              if (!targetPool) {
                return null;
              }
              const pathD = `M 176 ${source.y} C 242 ${source.y}, 272 ${targetPool.y}, ${targetPool.x - 66} ${targetPool.y}`;
              const lit = !activeUserFilter || activeUserFilter.subs.includes(source.id);

              return (
                <g key={source.id} style={{ opacity: lit ? 1 : 0.18, transition: "opacity 180ms ease" }}>
                  <path
                    d={pathD}
                    fill="none"
                    stroke="var(--rule)"
                    strokeWidth={queueActive || poolActive ? "1.8" : "1.5"}
                    strokeDasharray="5 4"
                    markerEnd={`url(#l1-arrow-rule-${uid})`}
                  />
                  <circle r="2.7" fill="var(--rule)" opacity="0.9" className="landing-flow-dot">
                    <animateMotion
                      dur={`${2.2 + (index % 3) * 0.35}s`}
                      repeatCount="indefinite"
                      path={pathD}
                    />
                  </circle>
                </g>
              );
            })}

            {/* Middle Shared Pool ovals */}
            {MACRO_POOL_NODES.map((poolNode) => {
              const lit = !activeUserFilter || activeUserFilter.subs.includes(poolNode.id);
              return (
                <g
                  key={poolNode.id}
                  role="button"
                  tabIndex={0}
                  aria-label={`第二步：${poolNode.name} 共享去重`}
                  className="cursor-pointer focus:outline-none"
                  style={{ opacity: lit ? 1 : 0.22, transition: "opacity 180ms ease" }}
                  onClick={() => onSelectMacroStage(1)}
                  onMouseEnter={() => onSelectMacroStage(1)}
                  onKeyDown={(event) => handleKeyTrigger(event, () => onSelectMacroStage(1))}
                >
                  <ellipse
                    cx={poolNode.x}
                    cy={poolNode.y}
                    rx="66"
                    ry="23"
                    fill="var(--surface)"
                    stroke={poolActive ? "var(--rule)" : "var(--line-strong)"}
                    strokeWidth={poolActive ? "1.9" : "1.4"}
                  />
                  <text
                    x={poolNode.x}
                    y={poolNode.y + 4}
                    textAnchor="middle"
                    fill="var(--ink)"
                    fontSize="12"
                    fontWeight="600"
                  >
                    {poolNode.name}
                  </text>
                </g>
              );
            })}

            {/* Pool -> Multi-User Processing solid fan-out curves + signal dots */}
            {MACRO_USER_LANES.flatMap((lane) =>
              lane.subs.map((sourceId, index) => {
                const poolNode = MACRO_POOL_NODES.find((node) => node.id === sourceId);
                if (!poolNode) {
                  return null;
                }
                const x1 = poolNode.x + 66;
                const y1 = poolNode.y;
                const x2 = 640;
                const y2 = lane.y;
                const pathD = `M ${x1} ${y1} C ${x1 + 78} ${y1}, ${x2 - 84} ${y2}, ${x2} ${y2}`;
                const lit = !hoveredUserId || hoveredUserId === lane.id;

                return (
                  <g
                    key={`${lane.id}-${sourceId}`}
                    style={{ opacity: hoveredUserId ? (lit ? 1 : 0.12) : 0.82, transition: "opacity 180ms ease" }}
                  >
                    <path
                      d={pathD}
                      fill="none"
                      stroke={lane.strokeVar}
                      strokeWidth={hoveredUserId === lane.id ? "2.6" : "1.7"}
                      markerEnd={`url(#l1-arrow-${lane.arrowSuffix}-${uid})`}
                    />
                    <circle r="2.8" fill={lane.strokeVar} className="landing-flow-dot">
                      <animateMotion dur={`${1.9 + index * 0.35}s`} repeatCount="indefinite" path={pathD} />
                    </circle>
                  </g>
                );
              })
            )}

            {/* Clickable 【私有加工】 zoom hotspots */}
            {MACRO_USER_LANES.map((lane) => {
              const lit = !hoveredUserId || hoveredUserId === lane.id;
              const isHighlighted = hoveredUserId === lane.id || processActive;

              return (
                <g
                  key={lane.id}
                  role="button"
                  tabIndex={0}
                  aria-label={`放大查看 ${lane.name} 的私有加工四道漏斗`}
                  className="cursor-pointer focus:outline-none"
                  style={{ opacity: hoveredUserId ? (lit ? 1 : 0.28) : 1, transition: "opacity 180ms ease" }}
                  onMouseEnter={() => {
                    setHoveredUserId(lane.id);
                    onSelectMacroStage(2);
                  }}
                  onMouseLeave={() => setHoveredUserId(null)}
                  onClick={() => onDrillDown(lane.id)}
                  onKeyDown={(event) => handleKeyTrigger(event, () => onDrillDown(lane.id))}
                >
                  <rect
                    x="642"
                    y={lane.y - 34}
                    width="178"
                    height="68"
                    rx="14"
                    fill="var(--surface)"
                    stroke={isHighlighted ? "var(--clay)" : lane.strokeVar}
                    strokeWidth={hoveredUserId === lane.id ? "2.4" : "1.8"}
                  />
                  <rect x="654" y={lane.y - 25} width="74" height="17" rx="8.5" fill="var(--clay-soft)" />
                  <text x="691" y={lane.y - 13.5} textAnchor="middle" fill="var(--clay)" fontSize="9.5" fontWeight="700">
                    订阅后加工
                  </text>
                  <rect
                    x="742"
                    y={lane.y - 25}
                    width="66"
                    height="17"
                    rx="8.5"
                    fill={hoveredUserId === lane.id ? "var(--clay-soft)" : "var(--bg-sunk)"}
                  />
                  <text
                    x="775"
                    y={lane.y - 13.5}
                    textAnchor="middle"
                    fill={hoveredUserId === lane.id ? "var(--clay)" : "var(--ink-2)"}
                    fontSize="9.5"
                    fontWeight="700"
                  >
                    放大 ⤢
                  </text>
                  <text x="731" y={lane.y + 8} textAnchor="middle" fill="var(--ink)" fontSize="13.5" fontWeight="700">
                    {lane.name} · 私有加工
                  </text>
                  <text x="731" y={lane.y + 24} textAnchor="middle" fill="var(--ink-3)" fontSize="10">
                    {lane.ruleHint}
                  </text>
                </g>
              );
            })}

            {/* Process -> Private Workspace lines */}
            {MACRO_USER_LANES.map((lane) => {
              const pathD = `M 822 ${lane.y} L 892 ${lane.y}`;
              const lit = !hoveredUserId || hoveredUserId === lane.id;

              return (
                <g
                  key={lane.id}
                  style={{ opacity: hoveredUserId ? (lit ? 1 : 0.22) : 1, transition: "opacity 180ms ease" }}
                >
                  <path
                    d={pathD}
                    fill="none"
                    stroke="var(--accent)"
                    strokeWidth={hoveredUserId === lane.id ? "2.3" : "1.8"}
                    markerEnd={`url(#l1-arrow-accent-${uid})`}
                  />
                  <circle r="2.5" fill="var(--accent)" className="landing-flow-dot">
                    <animateMotion dur="1.7s" repeatCount="indefinite" path={pathD} />
                  </circle>
                </g>
              );
            })}

            {/* Right User Output Workspaces */}
            {MACRO_USER_LANES.map((lane) => {
              const lit = !hoveredUserId || hoveredUserId === lane.id;
              return (
                <g
                  key={lane.id}
                  role="button"
                  tabIndex={0}
                  aria-label={`第四步：${lane.name} 的私人工作区`}
                  className="cursor-pointer focus:outline-none"
                  style={{ opacity: hoveredUserId ? (lit ? 1 : 0.28) : 1, transition: "opacity 180ms ease" }}
                  onMouseEnter={() => {
                    setHoveredUserId(lane.id);
                    onSelectMacroStage(3);
                  }}
                  onMouseLeave={() => setHoveredUserId(null)}
                  onClick={() => onSelectMacroStage(3)}
                  onKeyDown={(event) => handleKeyTrigger(event, () => onSelectMacroStage(3))}
                >
                  <rect
                    x="898"
                    y={lane.y - 27}
                    width="146"
                    height="54"
                    rx="14"
                    fill="var(--surface)"
                    stroke={workspaceActive && lit ? "var(--accent)" : "var(--line-strong)"}
                    strokeWidth={hoveredUserId === lane.id ? "2" : "1.5"}
                  />
                  <text x="971" y={lane.y - 3} textAnchor="middle" fill="var(--ink)" fontSize="14" fontWeight="700">
                    {lane.name}
                  </text>
                  <text x="971" y={lane.y + 15} textAnchor="middle" fill="var(--accent)" fontSize="10.5" fontWeight="600">
                    {lane.outputLabel}
                  </text>
                </g>
              );
            })}
          </svg>
        </div>

        {/* Mobile & tablet compact macro flow */}
        <div className="space-y-3 lg:hidden">
          <button
            type="button"
            onClick={() => onSelectMacroStage(0)}
            className={`w-full rounded-card border p-3.5 text-left transition-colors ${
              queueActive ? "border-rule/60 bg-surface" : "border-line bg-bg-sunk/40"
            }`}
          >
            <div className="flex items-center justify-between">
              <span className="mono text-[10.5px] font-semibold text-rule">1 · 活跃信源队列（并发≤2）</span>
              <span className="text-[10.5px] text-ink-4">同源仅抓 1 次</span>
            </div>
            <div className="mt-2 flex flex-wrap gap-1.5">
              {MACRO_QUEUE_SOURCES.map((source) => (
                <span
                  key={source.id}
                  className="rounded-lg border border-line-soft bg-surface px-2 py-1 text-[11.5px] font-medium text-ink-2"
                >
                  {source.name}
                </span>
              ))}
            </div>
          </button>

          <button
            type="button"
            onClick={() => onSelectMacroStage(1)}
            className={`w-full rounded-card border p-3 text-left transition-colors ${
              poolActive ? "border-rule/60 bg-surface" : "border-line bg-bg-sunk/35"
            }`}
          >
            <div className="flex items-center justify-between">
              <span className="mono text-[10.5px] font-semibold text-ink-2">2 · 共享去重落库池</span>
              <span className="text-[10.5px] text-accent">articles · discoveries</span>
            </div>
          </button>

          <div className="grid gap-2.5">
            {MACRO_USER_LANES.map((lane) => (
              <button
                key={lane.id}
                type="button"
                onClick={() => onDrillDown(lane.id)}
                className="flex items-center justify-between gap-3 rounded-card border border-clay/45 bg-surface p-3.5 text-left transition-colors hover:border-clay"
              >
                <div>
                  <div className="text-[13px] font-semibold text-ink">
                    {lane.name} · 私有加工（{lane.subs.length} 个订阅）
                  </div>
                  <div className="mt-0.5 text-[11.5px] text-ink-3">
                    {lane.ruleHint} → {lane.outputLabel}
                  </div>
                </div>
                <span className="mono shrink-0 rounded-full bg-clay-soft px-2.5 py-1 text-[10.5px] font-semibold text-clay">
                  放大 ⤢
                </span>
              </button>
            ))}
          </div>
        </div>
      </div>
    );
  }

  // L2: Unified SVG 4-Stage Funnel (matching L1's exact visual geometry, arrowheads, and animated pulses)
  const stage0 = activeDetailStage === 0;
  const stage1 = activeDetailStage === 1;
  const stage2 = activeDetailStage === 2;
  const stage3 = activeDetailStage === 3;
  const isAllBranch = selectedUser.filterMode === "all";
  const opDefault = isAllBranch ? 0.52 : 1;
  const opAll = isAllBranch ? 1 : 0.58;

  const dSplitTop = "M 366 202 C 394 202, 392 124, 420 124";
  const dSplitBot = "M 366 202 C 394 202, 392 280, 420 280";
  const dMergeTop = "M 584 124 C 614 124, 608 202, 636 202";
  const dMergeBot = "M 584 280 C 614 280, 608 202, 636 202";
  const dDeep = "M 796 202 L 836 202";
  const dOutHot = "M 992 202 C 1014 202, 1012 130, 1034 130";
  const dOutDaily = "M 992 202 C 1014 202, 1012 274, 1034 274";

  return (
    <div
      key="detail-level"
      className="landing-pipeline-zoom mt-7 rounded-[22px] border border-line bg-surface/90 p-4 shadow-[var(--shadow-card)] backdrop-blur-xs sm:p-5"
    >
      {/* Compact Back Button (clean single-action return to L1) */}
      <div className="mb-3.5 flex items-center">
        <button
          type="button"
          onClick={onBackToMacro}
          className="inline-flex items-center gap-1.5 rounded-full border border-line bg-surface px-3 py-1 text-[12px] font-semibold text-ink transition-colors hover:border-ink-4"
        >
          <IconArrowLeft size={13} />
          收回全站全景图
        </button>
      </div>

      {/* Desktop unified SVG 4-stage funnel */}
      <div className="hidden lg:block">
        <div className="mb-3 grid grid-cols-[1.08fr_auto_1fr] items-center gap-3 text-[11px]">
          <div className="mono flex items-center justify-between rounded-full border border-rule/30 bg-rule-softer px-3.5 py-1.5 font-semibold text-rule">
            <span className="uppercase tracking-[.1em]">0 TOKEN · 平台共享采集与本地规则降噪</span>
            <span className="font-normal text-ink-3">不消耗个人额度</span>
          </div>
          <span className="mono text-[10px] font-semibold uppercase tracking-[.14em] text-clay">
            隔离边界
          </span>
          <div className="mono flex items-center justify-between rounded-full border border-clay/30 bg-clay-softer px-3.5 py-1.5 font-semibold text-clay">
            <span className="uppercase tracking-[.1em]">BYOK · 个人大模型整理（账号严格隔离）</span>
            <span className="font-normal text-ink-3">仅精选文章调用</span>
          </div>
        </div>

        <svg viewBox="0 0 1180 404" className="h-auto w-full select-none overflow-visible">
          <defs>
            <marker
              id={`l2-arrow-rule-${uid}`}
              viewBox="0 0 10 10"
              refX="8"
              refY="5"
              markerWidth="6"
              markerHeight="6"
              orient="auto-start-reverse"
            >
              <path d="M 1 1.5 L 8.5 5 L 1 8.5 z" fill="var(--rule)" />
            </marker>
            <marker
              id={`l2-arrow-clay-${uid}`}
              viewBox="0 0 10 10"
              refX="8"
              refY="5"
              markerWidth="6"
              markerHeight="6"
              orient="auto-start-reverse"
            >
              <path d="M 1 1.5 L 8.5 5 L 1 8.5 z" fill="var(--clay)" />
            </marker>
            <marker
              id={`l2-arrow-accent-${uid}`}
              viewBox="0 0 10 10"
              refX="8"
              refY="5"
              markerWidth="6"
              markerHeight="6"
              orient="auto-start-reverse"
            >
              <path d="M 1 1.5 L 8.5 5 L 1 8.5 z" fill="var(--accent)" />
            </marker>
            <marker
              id={`l2-arrow-hot-${uid}`}
              viewBox="0 0 10 10"
              refX="8"
              refY="5"
              markerWidth="6"
              markerHeight="6"
              orient="auto-start-reverse"
            >
              <path d="M 1 1.5 L 8.5 5 L 1 8.5 z" fill="var(--hot)" />
            </marker>
            <marker
              id={`l2-arrow-amber-${uid}`}
              viewBox="0 0 10 10"
              refX="8"
              refY="5"
              markerWidth="6"
              markerHeight="6"
              orient="auto-start-reverse"
            >
              <path d="M 1 1.5 L 8.5 5 L 1 8.5 z" fill="var(--amber)" />
            </marker>
          </defs>

          {/* Clean vertical dashed isolation boundary between Step 02 and Step 03 */}
          <line
            x1="610"
            y1="8"
            x2="610"
            y2="396"
            stroke="var(--clay)"
            strokeWidth="1.5"
            strokeDasharray="6 5"
            opacity="0.72"
          />

          {/* Col 1: Source Inputs container */}
          <rect
            x="12"
            y="20"
            width="156"
            height="364"
            rx="18"
            fill="var(--surface)"
            stroke="var(--line)"
            strokeWidth="1.4"
          />
          <text
            x="90"
            y="42"
            textAnchor="middle"
            fill="var(--ink-4)"
            fontSize="10.5"
            fontWeight="600"
            letterSpacing="0.8"
          >
            多源输入通道
          </text>

          {L2_INPUT_SOURCES.map((source) => (
            <g
              key={source.name}
              role="button"
              tabIndex={0}
              aria-label={`STEP 01 输入：${source.name}`}
              className="cursor-pointer focus:outline-none"
              onClick={() => onSelectDetailStage(0)}
              onMouseEnter={() => onSelectDetailStage(0)}
              onKeyDown={(event) => handleKeyTrigger(event, () => onSelectDetailStage(0))}
            >
              <rect
                x="24"
                y={source.y - 20}
                width="132"
                height="40"
                rx="20"
                fill="var(--bg)"
                stroke={stage0 ? "var(--rule)" : "var(--line-strong)"}
                strokeWidth={stage0 ? "1.6" : "1.2"}
              />
              <text x="38" y={source.y + 4.5} textAnchor="start" fill="var(--ink)" fontSize="12" fontWeight="600">
                {source.name}
              </text>
              <circle cx="141" cy={source.y} r="4.5" fill="var(--rule)" opacity={stage0 ? 1 : 0.7} />
            </g>
          ))}

          {/* Edges 1: Source Inputs -> STEP 01 */}
          {L2_INPUT_SOURCES.map((source, index) => {
            const pathD = `M 156 ${source.y} C 186 ${source.y}, 182 202, 210 202`;
            return (
              <g key={source.name}>
                <path
                  d={pathD}
                  fill="none"
                  stroke="var(--rule)"
                  strokeWidth={stage0 ? "2.2" : "1.5"}
                  strokeDasharray="5 4"
                  opacity={stage0 ? 0.95 : 0.65}
                  markerEnd={`url(#l2-arrow-rule-${uid})`}
                />
                <circle r="2.6" fill="var(--rule)" opacity={stage0 ? 1 : 0.7} className="landing-flow-dot">
                  <animateMotion dur={`${1.8 + index * 0.25}s`} repeatCount="indefinite" path={pathD} />
                </circle>
              </g>
            );
          })}

          {/* Col 2: STEP 01 去重汇总 */}
          <g
            role="button"
            tabIndex={0}
            aria-label="STEP 01 去重汇总"
            className="cursor-pointer focus:outline-none"
            onClick={() => onSelectDetailStage(0)}
            onMouseEnter={() => onSelectDetailStage(0)}
            onKeyDown={(event) => handleKeyTrigger(event, () => onSelectDetailStage(0))}
          >
            <rect
              x="214"
              y="128"
              width="152"
              height="148"
              rx="14"
              fill="var(--surface)"
              stroke={stage0 ? "var(--rule)" : "var(--line)"}
              strokeWidth={stage0 ? "2.2" : "1.5"}
            />
            <text x="228" y="154" fill="var(--rule)" fontSize="10.5" fontWeight="700" letterSpacing="0.6">
              STEP 01
            </text>
            <rect x="294" y="139" width="58" height="20" rx="10" fill="var(--rule-soft)" />
            <text x="323" y="152.5" textAnchor="middle" fill="var(--rule)" fontSize="10" fontWeight="700">
              共享一次
            </text>
            <text x="290" y="192" textAnchor="middle" fill="var(--ink)" fontSize="15" fontWeight="700">
              去重汇总
            </text>
            <text x="290" y="218" textAnchor="middle" fill="var(--ink-3)" fontSize="11.5">
              URL 规范化合并报道
            </text>
            <text x="290" y="236" textAnchor="middle" fill="var(--ink-3)" fontSize="11.5">
              拦截老旧回填文章
            </text>
            <rect x="234" y="248" width="112" height="18" rx="9" fill="var(--bg-sunk)" />
            <text x="290" y="260.5" textAnchor="middle" fill="var(--ink-4)" fontSize="10" fontWeight="600">
              articles · discoveries
            </text>
          </g>

          {/* Edges 2: STEP 01 -> Dual Rule Branches */}
          <g style={{ opacity: opDefault, transition: "opacity 180ms ease" }}>
            <path
              d={dSplitTop}
              fill="none"
              stroke="var(--rule)"
              strokeWidth={!isAllBranch && (stage0 || stage1) ? "2.4" : "1.6"}
              markerEnd={`url(#l2-arrow-rule-${uid})`}
            />
            {!isAllBranch && (
              <circle r="2.7" fill="var(--rule)" className="landing-flow-dot">
                <animateMotion dur="1.9s" repeatCount="indefinite" path={dSplitTop} />
              </circle>
            )}
          </g>
          <g style={{ opacity: opAll, transition: "opacity 180ms ease" }}>
            <path
              d={dSplitBot}
              fill="none"
              stroke="var(--rule)"
              strokeWidth={isAllBranch && (stage0 || stage1) ? "2.4" : "1.5"}
              strokeDasharray="5 4"
              markerEnd={`url(#l2-arrow-rule-${uid})`}
            />
            {isAllBranch && (
              <circle r="2.7" fill="var(--rule)" className="landing-flow-dot">
                <animateMotion dur="1.9s" repeatCount="indefinite" path={dSplitBot} />
              </circle>
            )}
          </g>

          {/* Col 3 Upper: 默认过滤 · 标签规则初筛 */}
          <g
            role="button"
            tabIndex={0}
            aria-label="STEP 02 标签规则初筛"
            className="cursor-pointer focus:outline-none"
            style={{ opacity: opDefault, transition: "opacity 180ms ease" }}
            onClick={() => onSelectDetailStage(1)}
            onMouseEnter={() => onSelectDetailStage(1)}
            onKeyDown={(event) => handleKeyTrigger(event, () => onSelectDetailStage(1))}
          >
            <rect
              x="424"
              y="62"
              width="160"
              height="124"
              rx="14"
              fill="var(--surface)"
              stroke={!isAllBranch || stage1 ? "var(--rule)" : "var(--line)"}
              strokeWidth={!isAllBranch && stage1 ? "2.2" : "1.6"}
            />
            <text x="438" y="87" fill="var(--rule)" fontSize="10.5" fontWeight="700">
              默认过滤
            </text>
            <rect x="516" y="73" width="56" height="19" rx="9.5" fill="var(--ok-soft)" />
            <text x="544" y="86" textAnchor="middle" fill="var(--ok-ink)" fontSize="10" fontWeight="700">
              0 Token
            </text>
            <text x="504" y="124" textAnchor="middle" fill="var(--ink)" fontSize="14.5" fontWeight="700">
              标签规则初筛
            </text>
            <text x="504" y="148" textAnchor="middle" fill="var(--ink-3)" fontSize="11.5">
              关键词 · 公司实体
            </text>
            <text x="504" y="166" textAnchor="middle" fill="var(--ink-3)" fontSize="11.5">
              订阅标签本地匹配
            </text>
          </g>

          {/* Col 3 Lower: 全量跟踪 · 跳过标签过滤 */}
          <g
            role="button"
            tabIndex={0}
            aria-label="STEP 02 跳过标签过滤"
            className="cursor-pointer focus:outline-none"
            style={{ opacity: opAll, transition: "opacity 180ms ease" }}
            onClick={() => onSelectDetailStage(1)}
            onMouseEnter={() => onSelectDetailStage(1)}
            onKeyDown={(event) => handleKeyTrigger(event, () => onSelectDetailStage(1))}
          >
            <rect
              x="424"
              y="218"
              width="160"
              height="124"
              rx="14"
              fill="var(--bg-sunk)"
              stroke={isAllBranch ? "var(--rule)" : "var(--line-strong)"}
              strokeWidth={isAllBranch ? "2" : "1.4"}
              strokeDasharray="5 4"
            />
            <text x="438" y="243" fill="var(--ink-2)" fontSize="10.5" fontWeight="700">
              全量跟踪
            </text>
            <rect x="514" y="229" width="58" height="19" rx="9.5" fill="var(--surface)" />
            <text x="543" y="242" textAnchor="middle" fill="var(--ink-3)" fontSize="10" fontWeight="600">
              可选开关
            </text>
            <text x="504" y="280" textAnchor="middle" fill="var(--ink)" fontSize="14.5" fontWeight="700">
              跳过标签过滤
            </text>
            <text x="504" y="304" textAnchor="middle" fill="var(--ink-3)" fontSize="11.5">
              重要信源全量直通
            </text>
            <text x="504" y="322" textAnchor="middle" fill="var(--ink-3)" fontSize="11.5">
              不丢漏任何更新
            </text>
          </g>

          {/* Edges 3: Cross Isolation Boundary -> STEP 03 */}
          <g style={{ opacity: opDefault, transition: "opacity 180ms ease" }}>
            <path
              d={dMergeTop}
              fill="none"
              stroke="var(--clay)"
              strokeWidth={!isAllBranch ? "2.4" : "1.5"}
              markerEnd={`url(#l2-arrow-clay-${uid})`}
            />
            {!isAllBranch && (
              <circle r="2.7" fill="var(--clay)" className="landing-flow-dot">
                <animateMotion dur="1.8s" repeatCount="indefinite" path={dMergeTop} />
              </circle>
            )}
          </g>
          <g style={{ opacity: opAll, transition: "opacity 180ms ease" }}>
            <path
              d={dMergeBot}
              fill="none"
              stroke="var(--clay)"
              strokeWidth={isAllBranch ? "2.4" : "1.5"}
              markerEnd={`url(#l2-arrow-clay-${uid})`}
            />
            {isAllBranch && (
              <circle r="2.7" fill="var(--clay)" className="landing-flow-dot">
                <animateMotion dur="1.8s" repeatCount="indefinite" path={dMergeBot} />
              </circle>
            )}
          </g>

          {/* Col 4: STEP 03 Batch Triage */}
          <g
            role="button"
            tabIndex={0}
            aria-label="STEP 03 Batch Triage 批量初筛"
            className="cursor-pointer focus:outline-none"
            onClick={() => onSelectDetailStage(2)}
            onMouseEnter={() => onSelectDetailStage(2)}
            onKeyDown={(event) => handleKeyTrigger(event, () => onSelectDetailStage(2))}
          >
            <rect
              x="640"
              y="120"
              width="156"
              height="164"
              rx="14"
              fill="var(--surface)"
              stroke="var(--clay)"
              strokeWidth={stage2 ? "2.4" : "1.7"}
            />
            <text x="654" y="146" fill="var(--clay)" fontSize="10.5" fontWeight="700">
              STEP 03
            </text>
            <rect x="726" y="132" width="58" height="20" rx="10" fill="var(--clay-soft)" />
            <text x="755" y="145.5" textAnchor="middle" fill="var(--clay)" fontSize="10" fontWeight="700">
              省 ~80%
            </text>
            <text x="718" y="182" textAnchor="middle" fill="var(--ink)" fontSize="14.5" fontWeight="700">
              Batch Triage
            </text>
            <text x="718" y="206" textAnchor="middle" fill="var(--ink-3)" fontSize="11.5">
              &gt; 5 篇时 25 选 5
            </text>
            <text x="718" y="224" textAnchor="middle" fill="var(--ink-3)" fontSize="11.5">
              打包剔除水文与重复
            </text>
            <rect x="656" y="246" width="124" height="22" rx="11" fill="var(--clay-soft)" />
            <text x="718" y="260.5" textAnchor="middle" fill="var(--clay)" fontSize="10" fontWeight="700">
              ≤ 5 篇直接精读
            </text>
          </g>

          {/* Edge 4: STEP 03 -> STEP 04 */}
          <path
            d={dDeep}
            fill="none"
            stroke="var(--accent)"
            strokeWidth={stage2 || stage3 ? "2.4" : "1.7"}
            markerEnd={`url(#l2-arrow-accent-${uid})`}
          />
          <circle r="2.7" fill="var(--accent)" className="landing-flow-dot">
            <animateMotion dur="1.5s" repeatCount="indefinite" path={dDeep} />
          </circle>

          {/* Col 5: STEP 04 个人 LLM 精读 */}
          <g
            role="button"
            tabIndex={0}
            aria-label="STEP 04 个人 LLM 精读"
            className="cursor-pointer focus:outline-none"
            onClick={() => onSelectDetailStage(3)}
            onMouseEnter={() => onSelectDetailStage(3)}
            onKeyDown={(event) => handleKeyTrigger(event, () => onSelectDetailStage(3))}
          >
            <rect
              x="840"
              y="130"
              width="152"
              height="144"
              rx="14"
              fill="var(--surface)"
              stroke="var(--accent)"
              strokeWidth={stage3 ? "2.4" : "1.7"}
            />
            <text x="854" y="156" fill="var(--accent)" fontSize="10.5" fontWeight="700">
              STEP 04
            </text>
            <rect x="920" y="142" width="60" height="20" rx="10" fill="var(--bg-sunk)" />
            <text x="950" y="155.5" textAnchor="middle" fill="var(--ink-3)" fontSize="10" fontWeight="700">
              100次/日
            </text>
            <text x="916" y="192" textAnchor="middle" fill="var(--ink)" fontSize="14.5" fontWeight="700">
              个人 LLM 精读
            </text>
            <text x="916" y="216" textAnchor="middle" fill="var(--ink-3)" fontSize="11.5">
              生成 0-100 相关性分
            </text>
            <text x="916" y="234" textAnchor="middle" fill="var(--ink-3)" fontSize="11.5">
              中文标题与核心摘要
            </text>
            <rect x="860" y="246" width="112" height="16" rx="8" fill="var(--accent-soft)" />
            <text x="916" y="257.5" textAnchor="middle" fill="var(--accent)" fontSize="9.5" fontWeight="700">
              BYOK · 自有 Key
            </text>
          </g>

          {/* Edges 5: STEP 04 -> Outputs */}
          <path
            d={dOutHot}
            fill="none"
            stroke="var(--hot)"
            strokeWidth={stage3 ? "2.4" : "1.7"}
            markerEnd={`url(#l2-arrow-hot-${uid})`}
          />
          <circle r="2.6" fill="var(--hot)" className="landing-flow-dot">
            <animateMotion dur="1.8s" repeatCount="indefinite" path={dOutHot} />
          </circle>
          <path
            d={dOutDaily}
            fill="none"
            stroke="var(--amber)"
            strokeWidth={stage3 ? "2.4" : "1.7"}
            markerEnd={`url(#l2-arrow-amber-${uid})`}
          />
          <circle r="2.6" fill="var(--amber)" className="landing-flow-dot">
            <animateMotion dur="2s" repeatCount="indefinite" path={dOutDaily} />
          </circle>

          {/* Col 6 Upper: 48h 热点榜 */}
          <g
            role="button"
            tabIndex={0}
            aria-label="48小时热点榜"
            className="cursor-pointer focus:outline-none"
            onClick={() => onSelectDetailStage(3)}
            onMouseEnter={() => onSelectDetailStage(3)}
            onKeyDown={(event) => handleKeyTrigger(event, () => onSelectDetailStage(3))}
          >
            <rect
              x="1038"
              y="64"
              width="130"
              height="128"
              rx="14"
              fill="var(--surface)"
              stroke={stage3 ? "var(--hot)" : "var(--line)"}
              strokeWidth={stage3 ? "2" : "1.5"}
            />
            <rect x="1050" y="76" width="52" height="19" rx="9.5" fill="var(--hot-soft)" />
            <text x="1076" y="89" textAnchor="middle" fill="var(--hot)" fontSize="10" fontWeight="700">
              TOP 10
            </text>
            <text x="1152" y="90" textAnchor="end" fill="var(--hot)" fontSize="11" fontWeight="700">
              HOT
            </text>
            <text x="1103" y="126" textAnchor="middle" fill="var(--ink)" fontSize="14" fontWeight="700">
              48h 热点榜
            </text>
            <text x="1103" y="148" textAnchor="middle" fill="var(--ink-3)" fontSize="11">
              按分排序 · 只读
            </text>
            <text x="1103" y="166" textAnchor="middle" fill="var(--ink-3)" fontSize="11">
              零额外模型消耗
            </text>
          </g>

          {/* Col 6 Lower: 每日简报 */}
          <g
            role="button"
            tabIndex={0}
            aria-label="每日简报 08:00"
            className="cursor-pointer focus:outline-none"
            onClick={() => onSelectDetailStage(3)}
            onMouseEnter={() => onSelectDetailStage(3)}
            onKeyDown={(event) => handleKeyTrigger(event, () => onSelectDetailStage(3))}
          >
            <rect
              x="1038"
              y="212"
              width="130"
              height="128"
              rx="14"
              fill="var(--surface)"
              stroke={stage3 ? "var(--amber)" : "var(--line)"}
              strokeWidth={stage3 ? "2" : "1.5"}
            />
            <rect x="1050" y="224" width="50" height="19" rx="9.5" fill="var(--amber-soft)" />
            <text x="1075" y="237" textAnchor="middle" fill="var(--amber)" fontSize="10" fontWeight="700">
              08:00
            </text>
            <text x="1152" y="238" textAnchor="end" fill="var(--amber)" fontSize="11" fontWeight="700">
              24H
            </text>
            <text x="1103" y="274" textAnchor="middle" fill="var(--ink)" fontSize="14" fontWeight="700">
              每日简报
            </text>
            <text x="1103" y="296" textAnchor="middle" fill="var(--ink-3)" fontSize="11">
              汇总此前 24h
            </text>
            <text x="1103" y="314" textAnchor="middle" fill="var(--ink-3)" fontSize="11">
              私有工作区阅读
            </text>
          </g>
        </svg>
      </div>

      {/* Mobile & tablet compact vertical funnel */}
      <div className="space-y-2.5 lg:hidden">
        <div className="flex items-center justify-between rounded-full border border-rule/25 bg-rule-softer px-3 py-1 text-[10.5px]">
          <span className="mono font-semibold uppercase tracking-[.1em] text-rule">
            0 TOKEN · 共享采集与规则降噪
          </span>
        </div>

        <button
          type="button"
          onClick={() => onSelectDetailStage(0)}
          className={`w-full rounded-card border p-3.5 text-left transition-colors ${
            stage0 ? "border-rule/60 bg-surface" : "border-line bg-bg-sunk/40"
          }`}
        >
          <div className="flex items-center justify-between">
            <span className="mono text-[10.5px] font-semibold text-rule">STEP 01 · 去重汇总</span>
            <span className="inline-flex items-center gap-1 text-[10.5px] text-ink-4">
              <IconRss size={11} /> 共享一次
            </span>
          </div>
          <p className="mt-1 text-[12px] text-ink-3">多信源同篇文章合并入库，拦截老旧回填。</p>
        </button>

        <div className="grid grid-cols-2 gap-2.5">
          <button
            type="button"
            onClick={() => onSelectDetailStage(1)}
            className={`rounded-card border p-3 text-left transition-colors ${
              !isAllBranch && stage1 ? "border-rule/60 bg-surface" : "border-line bg-bg-sunk/30"
            }`}
            style={{ opacity: opDefault }}
          >
            <div className="mono text-[10px] font-semibold text-rule">默认 · 0 TOKEN</div>
            <div className="mt-1 text-[13px] font-semibold text-ink">标签规则初筛</div>
            <p className="mt-0.5 text-[11px] text-ink-3">关键词 / 实体 / 标签匹配</p>
          </button>

          <button
            type="button"
            onClick={() => onSelectDetailStage(1)}
            className="rounded-card border border-dashed border-line-strong bg-bg-sunk/40 p-3 text-left"
            style={{ opacity: opAll }}
          >
            <div className="mono text-[10px] font-semibold text-ink-3">可选 · 全量跟踪</div>
            <div className="mt-1 text-[13px] font-semibold text-ink">跳过标签过滤</div>
            <p className="mt-0.5 text-[11px] text-ink-3">重要源直接放行</p>
          </button>
        </div>

        <div className="relative py-2">
          <div className="border-t border-dashed border-clay/45" />
          <span className="mono absolute left-1/2 top-1/2 -translate-x-1/2 -translate-y-1/2 rounded-full border border-clay/30 bg-surface px-2.5 py-0.5 text-[10px] font-semibold text-clay">
            BYOK 隔离边界
          </span>
        </div>

        <button
          type="button"
          onClick={() => onSelectDetailStage(2)}
          className={`w-full rounded-card border p-3.5 text-left transition-colors ${
            stage2 ? "border-clay/60 bg-surface" : "border-line bg-bg-sunk/40"
          }`}
        >
          <div className="flex items-center justify-between">
            <span className="mono inline-flex items-center gap-1 text-[10.5px] font-semibold text-clay">
              <IconFilter size={11} /> STEP 03 · Batch Triage
            </span>
            <span className="rounded-full bg-clay-soft px-2 py-0.5 text-[10px] font-medium text-clay">
              省 ~80% Token
            </span>
          </div>
          <p className="mt-1 text-[12px] text-ink-3">大于 5 篇时 25 选 5，剔除水文重复；5 篇及以下直通精读。</p>
        </button>

        <button
          type="button"
          onClick={() => onSelectDetailStage(3)}
          className={`w-full rounded-card border p-3.5 text-left transition-colors ${
            stage3 ? "border-accent/60 bg-surface" : "border-line bg-bg-sunk/40"
          }`}
        >
          <div className="flex items-center justify-between">
            <span className="mono inline-flex items-center gap-1 text-[10.5px] font-semibold text-accent">
              <IconSparkles size={11} /> STEP 04 · 个人 LLM 精读
            </span>
            <span className="inline-flex items-center gap-1 text-[10px] text-hot">
              <IconFlame size={11} /> Top 10 · 08:00
            </span>
          </div>
          <p className="mt-1 text-[12px] text-ink-3">生成 0-100 相关性评分、中文标题与摘要，写入个人工作区。</p>
        </button>
      </div>
    </div>
  );
}
