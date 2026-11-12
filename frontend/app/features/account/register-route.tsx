import { RegisterPage, registerLoader } from "./auth-pages";
export const loader = registerLoader;
export const headers = () => ({ "Cache-Control": "private, no-store", "X-Robots-Tag": "noindex, nofollow" });
export const meta = () => [{ title: "创建账号 · MyHOT" }, { name: "robots", content: "noindex, nofollow" }];
export default RegisterPage;
