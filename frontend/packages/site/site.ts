// React site identity and user-facing copy. Update this file when changing the brand or page wording.
// Go owns cross-stack business configuration under backend-go/config and exposes frontend data through authenticated catalog endpoints.
// Configure the domain at deployment time through SITE_URL.

/**
 * Daily, weekly, and monthly publication times in Beijing time (HH:mm). A daily issue covers the 24 hours before its time;
 * weekly issues are published on Monday after each calendar week, and monthly issues on the first day of the month.
 * Scheduling, issue windows, missing-issue alerts, and time copy use these values. Files under public/ may use the
 * {{dailyTime}}, {{weeklyTime}}, and {{monthlyTime}} placeholders. The scheduler checks every 30 minutes, so use an hour or half-hour.
 */
export const EDITION_TIMES = { daily: "08:00", weekly: "10:00", monthly: "10:30" };

/** Publication times for copy such as "daily at 08:00", "every Monday at 10:00", and "on the first at 10:30". */
export const EDITION_WHEN = {
  daily: `每天 ${EDITION_TIMES.daily}`,
  weekly: `每周一 ${EDITION_TIMES.weekly}`,
  monthly: `每月 1 日 ${EDITION_TIMES.monthly}`,
};

export const SITE = {
  /** Site name used by navigation, page titles, share images, RSS, MCP, and the admin area. */
  name: "MyHOT",
  /**
   * Industry noun inserted into default phrases such as "AI daily" and "AI updates".
   * Replacing it with a term such as law, HR, or gold updates the corresponding page copy.
   */
  subject: "AI",
  /** Full homepage title used by browser tabs and search results. */
  homeTitle: "MyHOT — 你的 AI 行业热点工作区",
  /** Title of the topic directory page (/topics). */
  topicsTitle: "AI 主题：公司与模型、技术方向、内容形态的最新动态",
  /** Example shown in the feedback form input. */
  feedbackExample: "例如：我在搜索某个关键词时遇到……我原本想……",
  /** One-line description below the feedback page title. */
  feedbackLead: "发现 bug、想要的功能、看不顺眼的地方，都可以告诉我们。",
  /** Hint shown in the feedback form email field. */
  feedbackEmailHint: "留下邮箱，我们可以回信联系你",
  /** Short description used by search engines, share cards, RSS, and llms.txt. */
  description: "连接自己的 OpenAI 兼容模型，选择公司和关键词，MyHOT 从公共信息源中整理与你有关的热点和每日简报。",
  /** Optional detailed description below the short description in llms.txt. */
  llmsIntro: null as string | null,
  /** One-line caption shown below share images and posters. */
  tagline: "个人 AI 热点工作区",
  /** Keywords exposed to search engines in homepage structured data. */
  keywords: ["AI 资讯", "AI 新闻", "AI 日报", "AI 行业动态"] as string[],
  /** Optional year when the site began publishing, used in structured data. */
  since: null as string | null,
  /** Interface locale used by HTML lang and og:locale. */
  locale: "zh-CN",
  /** Fallback domain used only when SITE_URL is unset. */
  defaultUrl: "http://localhost:3000",
  /** Optional extra root-level icon filenames from frontend/brand/, for manifest.webmanifest or external references. */
  rootIcons: [] as string[],
  /**
   * Prefix for MCP tool names; use lowercase letters, digits, and underscores.
   * Do not change it after clients have integrated with these tools.
   */
  mcpPrefix: "myhot",
  /**
   * Version for public interfaces (MCP, OpenAPI, and llms.txt); only increase it.
   * Bump the major version when changing an existing field or its meaning, and document the change in deployment notes.
   */
  interfaceVersion: "4.0.0",
  /** Optional public contact email shown in llms.txt and the Agent usage guide. */
  contactEmail: null as string | null,
  /** Optional one-line note at the bottom of the About page. */
  footerNote: "连接自己的模型，整理关注的热点",
  /** Optional ICP filing number for sites in mainland China; links to the MIIT filing system when shown. */
  icp: null as string | null,
  /** Optional source repository URL; when set, the sidebar and account page show a GitHub link. */
  github: null as string | null,
  /** Site operator exposed to search engines in structured data. */
  organization: {
    name: "MyHOT",
    /** Optional founder details. */
    founder: null as null | { name: string; alternateName?: string; jobTitle?: string; description?: string; url?: string },
  },
  /** Name and version reported when fetching sources in the User-Agent; do not impersonate another site. */
  crawlerName: "MyHOTBot/1.0",
} as const;

/** Terms of use and privacy pages; their content lives under frontend/app/content/. */
export const POLICY = {
  terms: {
    /** Page name used by navigation, the footer, and the page title. */
    name: "使用规则",
    description: "MyHOT 网站、个人账号与信息处理规则。",
    /** Optional one-line description of this page in llms.txt. */
    covers: null as string | null,
    /** Optional usage guidance shown in the RSS and API sections of the Agent setup page. */
    notes: null as null | { rss: string; api: string },
    /**
     * Optional explanation of uses that require prior permission. In llms.txt it follows the copyright notice
     * in the usage section; in the Agent guide it appears at the start of the usage rules section.
     */
    license: null as null | { llms: string; agent: string },
    /**
     * Optional response headers declaring usage terms for public API, RSS, and OpenAPI files. They are appended
     * verbatim along with a Link header (rel="terms-of-service") pointing to this page.
     */
    headers: null as null | Record<string, string>,
  },
  privacy: {
    description: "MyHOT 如何处理账号资料、模型配置、个人结果与访问日志。",
    /** Optional one-line description of this page in llms.txt. */
    covers: null as string | null,
  },
} as const;

/** Wording and display options for item cards and detail pages. */
export const ITEM_COPY = {
  /** Label for the model's explanation, used on cards, detail pages, Markdown exports, Agent responses, and group notifications. */
  reasonLabel: "推荐理由",
  /** Whether readers see AI scores on pages and share images. This only affects display; public API and MCP still return scores. */
  showScore: true,
};

/** QR code card on the About page. */
interface ContactCard {
  kind: string;
  title: string;
  note: string;
  /** Optional root-level filename for a QR code linked from outside, such as qr-wechat.jpg; this URL always redirects to the current code. */
  alias?: string;
}

/** About page copy. Source, indexed-item, selected-item, and issue counts come from live site statistics. */
export const ABOUT = {
  kicker: `关于 ${SITE.name}`,
  /** Page description used by search results and share cards. */
  description: `关于 ${SITE.name}：${SITE.description}`,
  /** Main heading; the first line uses the normal color and the second uses the accent color. */
  headline: ["每天都有新动静，", "只整理你关注的。"] as [string, string],
  /** Copy below the heading. {sources} is replaced with the live source count and padded with spaces; sourcesFallback is used if statistics are unavailable. */
  lead: `${SITE.name} 从平台维护的{sources}个公共信源中查找与你关注项匹配的内容，用你配置的模型生成私人摘要和热点榜。每天北京时间 ${EDITION_TIMES.daily} 汇总近 24 小时的个人结果。`,
  sourcesFallback: "十几",
  /** Four steps shown below the source-river animation. */
  steps: {
    collect: "官方博客、媒体和个人的订阅源都在看；更新越勤的源看得越勤，最快 15 分钟看一次。",
    store: "抓到的都存下来，同一件事的报道归到一起，热点榜就是从这里算出来的。",
    select: "新文章命中你的关键词或公司实体后，才会提交给你自己的模型生成相关度评分、标题和摘要。",
    publish: "个人热点展示过去 48 小时评分最高的 10 条；每天北京时间 08:00 汇总前 24 小时的个人结果。",
  },
  /**
   * Optional author block; set to null to hide it.
   * avatarSourceId optionally points to an X account source whose avatar is used.
   * Upload the QR code in admin settings or place it under frontend/brand/contact/; the card is hidden when no code exists.
   */
  maker: null as null | {
    name: string;
    avatarSourceId?: string | null;
    greeting: string[];
    wechat?: ContactCard;
    feishu?: ContactCard;
  },
  /** Copyright and removal notice at the bottom of the page, followed by a link to the feedback page. */
  copyright: [`${SITE.name} 是聚合摘要和阅读索引，原文版权归各来源所有。如果你是来源方，希望更正、下架或调整展示方式，可以通过`, "联系我们。"] as [string, string],
  /** Optional anchor ID for the footer's terms link; keep it stable if external documents refer to it. */
  termsAnchor: null as string | null,
} as const;

/** Optional notices for administrators in the admin interface. */
export const ADMIN = {
  /** One-line notice below the feedback page title. */
  feedbackNote: null as string | null,
  /** Additional site-specific confirmation text when blocking a feedback source. */
  banNote: null as string | null,
  /** Additional site-specific confirmation text when changing a paid service request limit. */
  budgetNote: null as string | null,
};

/** Examples shown on the Agent setup page. */
export const AGENT = {
  /** Search row in the MCP tools table: searchable content and example queries. */
  search: { scope: "按公司、产品、人物或话题搜最近 7 天", ask: "这家公司最近发了什么？" },
};

/** Wording used in daily, weekly, and monthly report layouts. */
export const REPORTS = {
  /** Publisher line below the report masthead. */
  imprint: SITE.name.toUpperCase(),
  /** One-word label shown beside the report masthead. */
  motto: SITE.subject as string,
  /** Description for each report page, without a trailing period; also used for weekly and monthly issues in llms.txt. */
  descriptions: {
    daily: `${SITE.name} ${subjectAfter(`${EDITION_WHEN.daily}（北京时间）发布的`, "行业精编日报")}`,
    weekly: subjectAfter("每周", "行业综合回顾"),
    monthly: subjectAfter("每月", "行业盘点"),
  },
  /**
   * Label for one item in an issue (for example, "4 top stories"). Used in the no-lead headline, masthead and archive counts,
   * the fallback description for weekly and monthly issues, and the subscription description.
   */
  entry: { measure: "件", noun: "大事" },
  /** Labels following the other masthead counts; also used for selected-item and daily-issue counts on About and topic pages. */
  metricUnits: { sourcesCount: "个来源", firstPartyEvents: "件一手发布", selectedCount: "条精选", reportsCovered: "期日报" },
  /** Label for the total item count on report share images. */
  shareUnit: "件大事",
};

/** Deployment-specific wording in operational alerts sent only to the site owner. */
export const ALERTS = {
  /** Alert after this many minutes without a newly indexed article, up to one day; ALERT_QUIET_MINUTES takes precedence. */
  quietMinutes: 360,
  /** Optional baseline indexing-volume detail appended to a quiet-period alert. */
  usualFlow: null as string | null,
  /** Instructions for locating logs in a Worker-stopped alert. */
  workerLogs: "看 Go Worker 的日志（docker compose logs go-worker）",
  /** Steps named in alerts when a model provider rejects requests or runs out of quota; unspecified providers use generic wording. */
  modelStops: {} as Record<string, string>,
};

/** Text on page share images (/og/pages/*.png); the topic directory image is generated from the topic count. */
export const CARDS: Record<string, { kicker: string; title: string; subtitle: string; accent?: "hot" | "amber" }> = {
  site: { kicker: subjectAfter("每日", "精选"), title: SITE.tagline, subtitle: SITE.description },
  all: { kicker: subjectAfter("全部", "动态"), title: "所有信源的最新动态，一站看完", subtitle: "按时间汇总各信源的最新动态，可按类别与标签筛选。" },
  hot: { kicker: "热点榜", title: "过去 48 小时，大家在讨论什么", subtitle: "热度指数、趋势与组成热度的公开来源。", accent: "hot" },
  daily: { kicker: withSubject("日报"), title: subjectAfter(`每天 ${spokenTime(EDITION_TIMES.daily)}，一份读得完的`, "日报"), subtitle: `${subjectAfter("前一天值得关注的", "动态")}。` },
  weekly: { kicker: withSubject("周报"), title: `一周${REPORTS.entry.noun}，一次看清`, subtitle: "本周的主线、重要发布与值得回看的讨论。" },
  monthly: { kicker: withSubject("月报"), title: "一个月的变化", subtitle: "月度主线与关键事件回顾。" },
  about: { kicker: "关于", title: `关于 ${SITE.name}`, subtitle: SITE.description },
  terms: { kicker: "使用规则", title: `${SITE.name} 使用规则`, subtitle: "网页及已登录账号可访问接口的使用范围。" },
  privacy: { kicker: "隐私说明", title: `${SITE.name} 隐私说明`, subtitle: "访问日志、浏览器本地数据与反馈资料的处理方式。" },
  changelog: { kicker: "更新日志", title: `${SITE.name} 更新日志`, subtitle: "功能更新、优化、公告与下线记录。" },
  feedback: { kicker: "反馈", title: "告诉我们哪里可以更好", subtitle: "内容、功能、接入，或来源方的更正与下架请求。" },
  agent: { kicker: "Agent 接入", title: `把 ${SITE.name} 接进你的 Agent`, subtitle: "MCP、RSS、API 使用登录会话读取历史归档。" },
};

/** Deployment-specific access details included in the Agent guide and llms.txt. */
export const ACCESS = {
  /** Optional approximate requests per IP per minute before the reverse proxy returns 429 with Retry-After; null means unlimited and is omitted from the guide. */
  ratePerMinute: null as number | null,
  /** Optional User-Agent for clients that synchronize data programmatically, shown after the JSON API description. */
  userAgent: null as string | null,
};

/** Optional settings specific to this deployment. */
export const DEPLOYMENT = {
  /** Default repository-relative directory for credential group files (models.env, collectors.env, etc.); AIHOT_CREDENTIALS_DIR takes precedence, otherwise only process environment variables are read. */
  credentialsDir: null as string | null,
  /** Optional filenames for credential groups; unspecified groups use <group>.env, such as models.env. */
  credentialFiles: {} as Partial<Record<string, string>>,
  /** Additional required credentials for this deployment as [group, environment variable] pairs; checked at production API startup. */
  requiredSecrets: [] as const,
  /** Optional Host header expected by the production API (for CDN origin requests); local Web-to-API requests use it too. */
  originHost: null as string | null,
  /** Optional header used by the reverse proxy to preserve the original admin URL when redirecting unauthenticated users to login. */
  loginReturnHeader: null as string | null,
  /**
   * Bandwidth limits for fetching images through the image proxy. Uncached images return 503 after a limit is reached;
   * daily exhaustion is reported in the operations digest. Null means unlimited. IMGPROXY_UPSTREAM_MB_PER_MINUTE and
   * IMGPROXY_UPSTREAM_GB_PER_DAY environment variables take precedence.
   */
  imageUpstreamBudget: null as null | { mbPerMinute: number; gbPerDay: number },
  /**
   * Optional domains verified to connect directly from the server instead of using EGRESS_PROXY_URL, shared by collection and image fetching.
   * Routing is re-evaluated for each redirect target, and direct connections still validate the resolved address.
   */
  directFetchHosts: [] as string[],
  /**
   * Gold set options used when backend-go/cmd/eval-selection runs without arguments: file (relative to the repository root), sample count, split, and threshold sweep range.
   * Null uses all samples from .data/gold.jsonl (up to 200) and sweeps thresholds from 40 to 90.
   */
  selectionGold: null as null | { file: string; sample: number; split: string; sweep: [number, number] },
};

/** Join an industry noun and a report noun with a space when both are English; omit the space when the noun is Chinese. */
export function withSubject(noun: string): string {
  return /[A-Za-z0-9]$/.test(SITE.subject) ? `${SITE.subject} ${noun}` : `${SITE.subject}${noun}`;
}

/** Join an industry noun to Chinese text without a space and to English text with a space; append the noun through withSubject. */
export function subjectAfter(text: string, noun?: string): string {
  const gap = /^[A-Za-z0-9]/.test(SITE.subject) ? " " : "";
  return `${text}${gap}${noun ? withSubject(noun) : SITE.subject}`;
}

/** Format HH:mm as a spoken time, such as "8 o'clock" or "10:30". */
function spokenTime(time: string): string {
  const [hour, minute] = time.split(":").map(Number) as [number, number];
  return `${hour} 点${minute ? ` ${minute} 分` : ""}`;
}
