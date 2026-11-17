// Public vocabularies shared by the website, the API and the worker. The categories themselves belong to
// the industry pack (shared/industry/taxonomy.ts); their keys are external identities (URLs, API, RSS) and never change.
// How the public API, RSS and MCP differ from the website is the site's (shared/site/site.ts PUBLIC_CATEGORIES).
import { CATEGORIES } from "@aihot/industry/taxonomy";
import { PUBLIC_CATEGORIES } from "@aihot/site";

type Category = (typeof CATEGORIES)[number];

export type CategoryKey = string;
export const CATEGORY_KEYS = CATEGORIES.map((c) => c.key) as unknown as readonly [CategoryKey, ...CategoryKey[]];

/** Website tab labels. */
export const CATEGORY_LABELS = Object.fromEntries(CATEGORIES.map((c) => [c.key, c.label])) as Record<CategoryKey, string>;

const MERGE: Partial<Record<string, string>> = PUBLIC_CATEGORIES.merge;
const FEED_LABELS: Partial<Record<string, string>> = PUBLIC_CATEGORIES.feedLabels;

/** The categories the public API, RSS and MCP know: a merged category is published as the one it joins. */
export type PublicApiCategoryKey = string;
export const PUBLIC_API_CATEGORY_KEYS = CATEGORIES.filter((c) => !Object.hasOwn(MERGE, c.key)).map((c) => c.key) as unknown as readonly [PublicApiCategoryKey, ...PublicApiCategoryKey[]];

export function toPublicApiCategory(category: string | null): PublicApiCategoryKey | null {
  const c = CATEGORIES.find((x) => x.key === category);
  if (!c) return null;
  return (MERGE[c.key] ?? c.key) as PublicApiCategoryKey;
}

/** A public category's name in feeds (<category>, a category feed's title): the site's, else the pack's feed label, else its website label. */
export function feedCategoryLabel(key: PublicApiCategoryKey): string {
  const c: { label: string; feedLabel?: string } = CATEGORIES.find((x) => x.key === key)!;
  return FEED_LABELS[key] ?? c.feedLabel ?? c.label;
}

export function isCategoryKey(value: unknown): value is CategoryKey {
  return typeof value === "string" && (CATEGORY_KEYS as readonly string[]).includes(value);
}


export { CHANNEL_KEYS, CHANNEL_LABELS, isChannelKey, ARTICLE_ID_PATTERN } from "@aihot/contracts/taxonomy";
export type { ChannelKey } from "@aihot/contracts/taxonomy";
