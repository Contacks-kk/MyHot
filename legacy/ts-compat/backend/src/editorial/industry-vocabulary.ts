import entityCatalog from "../../../../../backend-go/config/entities.json" with { type: "json" };
import vocabularyCatalog from "../../../../../backend-go/config/vocabulary.json" with { type: "json" };

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
