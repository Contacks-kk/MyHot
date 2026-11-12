import { PasswordResetPage, passwordLoader } from "./auth-pages";
export const loader = passwordLoader;
export const headers = () => ({ "Cache-Control": "private, no-store", "X-Robots-Tag": "noindex, nofollow" });
export const meta = () => [{ title: "密码管理 · MyHOT" }, { name: "robots", content: "noindex, nofollow" }];
export default PasswordResetPage;
