// Client-side translation helper with session caching and graceful error fallback.
const CACHE_PREFIX = "aihot:tr:";
const memoryCache = new Map<string, string>();

/** Translate a text fragment into target language (default: English) without incurring backend model costs. */
export async function translateText(text: string, target: "en" | "zh" = "en"): Promise<string> {
  const trimmed = text.trim();
  if (!trimmed) {
    return "";
  }

  const cacheKey = `${target}:${trimmed}`;
  if (memoryCache.has(cacheKey)) {
    return memoryCache.get(cacheKey)!;
  }

  if (typeof window !== "undefined") {
    try {
      const stored = window.sessionStorage?.getItem(CACHE_PREFIX + cacheKey);
      if (stored) {
        memoryCache.set(cacheKey, stored);
        return stored;
      }
    } catch {
      // sessionStorage unavailable
    }
  }

  const url = `https://translate.googleapis.com/translate_a/single?client=gtx&sl=auto&tl=${target}&dt=t&q=${encodeURIComponent(trimmed)}`;
  try {
    const response = await fetch(url, {
      signal: AbortSignal.timeout(6000),
    });
    if (!response.ok) {
      throw new Error(`translate status ${response.status}`);
    }

    const data = await response.json();
    if (Array.isArray(data) && Array.isArray(data[0])) {
      const result = data[0]
        .map((segment: unknown) => (Array.isArray(segment) && typeof segment[0] === "string" ? segment[0] : ""))
        .join("");

      if (result) {
        memoryCache.set(cacheKey, result);
        if (typeof window !== "undefined") {
          try {
            window.sessionStorage?.setItem(CACHE_PREFIX + cacheKey, result);
          } catch {
            // storage full
          }
        }
        return result;
      }
    }
  } catch (err) {
    // Graceful fallback to original text
    console.warn("Translation fallback:", err);
  }

  return trimmed;
}
