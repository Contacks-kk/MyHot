/** Page paths shared by the production and development authentication gates. */
export function pagePath(pathname: string): string {
  const decoded = decodeURIComponent(pathname).replace(/\/+/g, "/").replace(/\.data$/, "");
  return decoded === "/_" ? "/" : decoded;
}

const ANONYMOUS_PAGES = new Set(["/", "/about", "/terms", "/privacy", "/login", "/register", "/verify-email", "/password-reset"]);
export function isAnonymousPage(pathname: string): boolean { return ANONYMOUS_PAGES.has(pagePath(pathname)); }

/** Return paths must remain on this origin after the browser normalizes them. */
export function safeReturnTo(target: string | null): string {
  return target && /^\/(?![\\/])/.test(target) && !/[\\\u0000-\u001f\u007f]/.test(target) ? target : "/";
}
