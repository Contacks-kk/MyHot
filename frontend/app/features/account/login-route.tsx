import { LoginPage, loginLoader } from "./auth-pages";
export const loader = loginLoader;
export const headers = () => ({ "Cache-Control": "private, no-store", "X-Robots-Tag": "noindex, nofollow" });
export const meta = () => [{ title: "登录 · MyHOT" }, { name: "robots", content: "noindex, nofollow" }];
export default LoginPage;
