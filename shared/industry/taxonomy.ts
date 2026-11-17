// 这个行业的分类体系：类别、标签词表、公司（主体）名录，以及防止张冠李戴的身份词典。
// 模型按这里的词表打标签，主题页（topics.json）按标签归类，筛选栏按类别分组。
// 换行业时：类别的 key 会出现在网址里（/all?category=…），上线后就不要再改；标签和名录可以随时增减。
import categoryCatalog from "../../backend-go/config/categories.json" with { type: "json" };

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

