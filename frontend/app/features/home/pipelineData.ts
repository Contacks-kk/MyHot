export type PipelineLevel = "macro" | "detail";

export interface PipelineStageInfo {
  step: string;
  title: string;
  badge: string;
  summary: string;
  detail: string;
  metricLabel: string;
  metricValue: string;
}

export interface MacroQueueSource {
  id: string;
  name: string;
  tag: string;
  y: number;
}

export interface MacroPoolNode {
  id: string;
  name: string;
  x: number;
  y: number;
}

export interface MacroUserLane {
  id: string;
  name: string;
  outputLabel: string;
  subs: readonly string[];
  ruleHint: string;
  filterMode: "default" | "all";
  y: number;
  strokeVar: string;
  arrowSuffix: string;
}

export const MACRO_QUEUE_SOURCES: readonly MacroQueueSource[] = [
  {
    id: "openai",
    name: "OpenAI Blog",
    tag: "RSS",
    y: 66,
  },
  {
    id: "anthropic",
    name: "Anthropic News",
    tag: "Web",
    y: 132,
  },
  {
    id: "nvidia",
    name: "NVIDIA Blog",
    tag: "RSS",
    y: 198,
  },
  {
    id: "google",
    name: "Google AI",
    tag: "RSS",
    y: 264,
  },
  {
    id: "meta",
    name: "Meta AI",
    tag: "X源",
    y: 330,
  },
  {
    id: "hf",
    name: "HuggingFace",
    tag: "RSS",
    y: 396,
  },
];

export const MACRO_POOL_NODES: readonly MacroPoolNode[] = [
  {
    id: "openai",
    name: "OpenAI 池",
    x: 356,
    y: 70,
  },
  {
    id: "anthropic",
    name: "Anthropic 池",
    x: 394,
    y: 136,
  },
  {
    id: "meta",
    name: "Meta 池",
    x: 334,
    y: 202,
  },
  {
    id: "google",
    name: "Google 池",
    x: 374,
    y: 268,
  },
  {
    id: "nvidia",
    name: "NVIDIA 池",
    x: 416,
    y: 334,
  },
  {
    id: "hf",
    name: "HF 开源池",
    x: 342,
    y: 400,
  },
];

export const MACRO_USER_LANES: readonly MacroUserLane[] = [
  {
    id: "u1",
    name: "张三",
    outputLabel: "48h 热点 · 日报",
    subs: ["openai", "anthropic", "meta"],
    ruleHint: "过滤: Agent / 推理",
    filterMode: "default",
    y: 96,
    strokeVar: "var(--clay)",
    arrowSuffix: "u1",
  },
  {
    id: "u2",
    name: "李四",
    outputLabel: "热点 · 私有 RSS",
    subs: ["meta", "google", "nvidia"],
    ruleHint: "全量跟踪 · 直通",
    filterMode: "all",
    y: 230,
    strokeVar: "var(--accent)",
    arrowSuffix: "u2",
  },
  {
    id: "u3",
    name: "王五",
    outputLabel: "08:00 日报 · MCP",
    subs: ["google", "hf"],
    ruleHint: "过滤: 开源 / 定价",
    filterMode: "default",
    y: 364,
    strokeVar: "var(--rule)",
    arrowSuffix: "u3",
  },
];

export const MACRO_STAGES: readonly PipelineStageInfo[] = [
  {
    step: "01",
    title: "WaitQueue 共享队列",
    badge: "仅抓活跃源 · 并发≤2",
    summary: "只调度至少被一位用户订阅的信源，按源级分布式锁与条件请求限频抓取。",
    detail: "无人订阅的历史信源保持休眠不占抓取代价；同一公共源无论被多少人订阅，站点内只发起一次 HTTP 拉取。",
    metricLabel: "公共调度队列",
    metricValue: "每队列最多 2 路并发 · 同源互斥",
  },
  {
    step: "02",
    title: "共享去重落库池",
    badge: "articles + discoveries",
    summary: "抓取内容经 URL 规范化与身份键去重，只存一份公共文章并记录发现来源映射。",
    detail: "当不同信源转载同一篇报道时，通过 discovery 关系保留全部发现路径，新订阅者无需等待再次采集即可复用。",
    metricLabel: "共享存储效率",
    metricValue: "同文仅存 1 份 · 跨源共享索引",
  },
  {
    step: "03",
    title: "按订阅各自私有加工",
    badge: "点击节点放大 4 步 ↗",
    summary: "后台每分钟将新增文章按各人订阅源与关键词规则分流，调用各自 BYOK 模型加工。",
    detail: "未配置个人模型不消费任何额度；进入个人支路后还会叠加 0-Token 规则初筛、Batch Triage 与精读评分。",
    metricLabel: "账号严格隔离",
    metricValue: "密钥 AES-256-GCM 加密 · 100 次/日上限",
  },
  {
    step: "04",
    title: "沉淀至个人只读工作区",
    badge: "网页 · RSS · MCP",
    summary: "加工结果写入各自隔离表，呈现为 48 小时热点 Top 10、08:00 日报与私有订阅接口。",
    detail: "个人结果与全站公开榜、他人数据彻底隔离；打开网页阅读或经由 RSS / MCP 拉取均不再额外消耗模型额度。",
    metricLabel: "私域消费入口",
    metricValue: "/hot Top 10 · /daily 日报 · /api/mcp",
  },
];

export const FUNNEL_STAGES: readonly PipelineStageInfo[] = [
  {
    step: "01",
    title: "共享去重采集",
    badge: "一次抓取 · 订阅共享",
    summary: "相同 RSS / Atom 源全站共用调度与条件请求，新增隔离源首次只放行 48 小时内最新 10 篇。",
    detail: "自动按规范化地址去重源池与文章指纹；历史过往文章归入回填库，防止一次导入上百篇打爆个人模型。",
    metricLabel: "原始进入本轮",
    metricValue: "142 篇 → 过滤旧回填后 38 篇",
  },
  {
    step: "02",
    title: "本地 0-Token 规则初筛",
    badge: "零模型消耗",
    summary: "优先在后台用关键词与公司实体词典做本地匹配，未命中的噪声文章直接跳过。",
    detail: "同时命中关键词或行业实体名称/别名才会保留；指定重要信源可开启全量跟踪直接放行。",
    metricLabel: "本地规则筛选",
    metricValue: "38 篇 → 命中 18 篇（0 次调用）",
  },
  {
    step: "03",
    title: "Batch Triage 批量初筛",
    badge: "省 ~80% Token",
    summary: "待加工超过 5 篇时，先将至多 25 篇标题与 180 字摘要打包成 1 次调用选出 Top 5。",
    detail: "替代对几十篇文章逐篇全文精读，在保留高价值更新的同时显著压低个人 API Key 消耗与等待耗时。",
    metricLabel: "批量初筛漏斗",
    metricValue: "18 篇 → 1 次调用精选 Top 5",
  },
  {
    step: "04",
    title: "私人精读与 08:00 日报",
    badge: "账号严格隔离",
    summary: "仅对精选文章调用你的 OpenAI 兼容模型打分写摘要，并在北京时间 08:00 汇成个人日报。",
    detail: "内置单日调用上限（默认 100 次/日）防止突发洪峰；所有评分、热点榜和简报均隔离在个人工作区中。",
    metricLabel: "最终产出结果",
    metricValue: "48h 热点榜 Top 10 · 08:00 简报",
  },
];
