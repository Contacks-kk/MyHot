// 这个行业的分类体系：类别、标签词表、公司（主体）名录，以及防止张冠李戴的身份词典。
// 模型按这里的词表打标签，主题页（topics.json）按标签归类，筛选栏按类别分组。
// 换行业时：类别的 key 会出现在网址里（/all?category=…），上线后就不要再改；标签和名录可以随时增减。
import entityCatalog from "./entities.json" with { type: "json" };
import categoryCatalog from "./categories.json" with { type: "json" };
import reportingCatalog from "./reporting.json" with { type: "json" };
import vocabularyCatalog from "./vocabulary.json" with { type: "json" };

/**
 * 网页上的类别（筛选栏、卡片角标、RSS 分类订阅）。key 是网址和接口里的身份，上线后不要改。
 * section 是日报里的分节标题（几个类别可以共用一节，按这里的顺序排）；guide 告诉结构抽取模型这一类收什么、
 * 和相邻类别的边界在哪（总的归类原则写在 prompts/structure.md 里）。
 * commentary 标出评论类（教程、观点）：日报写过的事又有评论类的后续报道，只占一行快讯（报道它的信源够多时除外）。
 * 没归上类的资料在日报里放进第一个 key 为 industry 的类别所在的节（没有就放最后一节）。
 * feedLabel 是分类 RSS 标题里的名字（不写就用 label）。公开接口、RSS 和 MCP 里要把一类并进另一类发布，写在站点设置里（shared/site/site.ts 的 PUBLIC_CATEGORIES）。
 */
const CATEGORY_KEY_VALUES = ["ai-models", "ai-products", "industry", "paper", "tip", "opinion"] as const;
type CategoryKey = (typeof CATEGORY_KEY_VALUES)[number];
type Category = { key: CategoryKey; label: string; feedLabel?: string; section: string; guide: string; commentary?: true };

if (JSON.stringify(categoryCatalog.map(({ key }) => key)) !== JSON.stringify(CATEGORY_KEY_VALUES)) {
  throw new Error("shared/industry/categories.json keys and taxonomy type keys must stay in sync");
}

/** Shared site category catalog: TypeScript owns the typed interface, Go reads the same JSON keys. */
export const CATEGORIES = categoryCatalog as unknown as readonly [Category, ...Category[]];

/**
 * 这个行业最受关注的一类发布（AI 行业是新模型）：日报报头的“N 个新模型”、改分类后修订已出的报告都按它数。
 * category 是类别，tag 是标签，两者都对上才算；unit 接在数字后面。
 * 没有这样一类的行业设成 null，报头就不显示这个数。
 */
export const RELEASE: { category: string; tag: string; unit: string } | null = reportingCatalog.release;

/** 周报月报的总述可以直接写、不必在报道里找到出处的行业通用词（小写）。站名会自动算进去。 */
export const PLAIN_TERMS: readonly string[] = reportingCatalog.plainTerms;

/**
 * 内容理解一步给每篇资料判的“内容类型”（写在 prompts/content-understanding.md 里，改了类型要同步改那份提示词）。
 * 评分提示词（prompts/selection-score.md）按类型给五个维度不同的权重。
 */
export const ITEM_TYPES = vocabularyCatalog.itemTypes as unknown as readonly [
  "model_release", "product_launch", "tool_or_prompt", "research_paper", "industry_event", "opinion_analysis", "tutorial_explainer",
];

// ── 标签词表 ────────────────────────────────────────────────────────────────────────────

/** 每篇资料的第一个标签必须是这些“分类标签”之一。 */
export const CATEGORY_TAGS = vocabularyCatalog.categoryTags as readonly string[];

/** 可选的主题标签。 */
export const TOPIC_TAGS = vocabularyCatalog.topicTags as readonly string[];

/** 可选的实体标签（公司、机构、平台）。 */
export const ENTITY_TAGS = vocabularyCatalog.entityTags as readonly string[];

/** 模型常写的近义词，统一成词表里的写法。 */
export const TAG_SYNONYMS = vocabularyCatalog.tagSynonyms as Readonly<Record<string, string>>;

// ── 公司与主体 ──────────────────────────────────────────────────────────────────────────

/**
 * 公司主题：id → 显示名、卡片上显示的标签（null 表示只用 entity:<id> 归类）、别名。
 * aliases 给结构抽取模型看；otherNames 是公司自己的其他称呼（官方账号名、子品牌），
 * 把事实的主体对到发布方时也认它们。
 */
export const ENTITIES = entityCatalog as Record<string, { name: string; displayTag: string | null; aliases: string[]; otherNames?: string[] }>;

/**
 * 身份词典：摘要和标题里出现的公司，必须在原文里也出现过，否则退回原标题、丢掉摘要（防止模型张冠李戴）。
 * 行业没有这个问题时可以留空数组。
 */
export const IDENTITY_LEXICON = vocabularyCatalog.identityLexicon.map((entry) => ({
  id: entry.id,
  name: entry.name,
  patterns: entry.patterns.map(({ source, flags }) => new RegExp(source, flags)),
}));

/** 这些域名上的文章，发布方就是对应的公司（托管平台如 GitHub、arXiv 不算）。 */
export const PUBLISHER_DOMAINS = vocabularyCatalog.publisherDomains;

/** 原文里的这些写法也算提到了对应公司。 */
export const IDENTITY_CONTEXT_ALIASES = vocabularyCatalog.identityContextAliases.map(({ entityId, source, flags }) => ({
  entityId,
  pattern: new RegExp(source, flags),
}));
