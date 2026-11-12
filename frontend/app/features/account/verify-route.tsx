import { VerifyEmailPage } from "./auth-pages";
export const headers = () => ({ "Cache-Control": "private, no-store", "X-Robots-Tag": "noindex, nofollow" });
export const meta = () => [{ title: "验证邮箱 · MyHOT" }, { name: "robots", content: "noindex, nofollow" }];
export default VerifyEmailPage;
