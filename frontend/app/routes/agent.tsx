import { BackLink } from "../components/ui/BackLink";
import { Link } from "react-router";
import { SITE } from "@aihot/site";
import type { Route } from "./+types/agent";
import type { Screen } from "../components/shell/screens";
export const handle: Screen = { tab: "me", name: "内容接口" };
export const headers: Route.HeadersFunction = () => ({ "Cache-Control": "private, no-store" });
export const meta: Route.MetaFunction = () => [{ title: `内容接口 · ${SITE.name}` }, { name: "robots", content: "noindex" }];
export default function AgentPage() {
  return <section className="mx-auto max-w-[760px] pb-10"><BackLink to="/hot" label="个人热点榜单" /><h1 className="mt-4 text-[25px] font-semibold text-ink">登录后的内容接口</h1><p className="mt-3 text-[13px] leading-7 text-ink-3">个人摘要、热点与日报通过当前账号的安全会话读取。首版使用浏览器工作区查看，个人结果不会进入原站 RSS、公开 API 或 MCP。</p>
    <div className="card mt-5 divide-y divide-line">{[["/api/me/feed","个人文章结果"],["/api/me/hot","过去48小时的个人热点榜"],["/api/me/daily/view","个人日报目录和所选一期正文"],["/api/me/history","原站只读历史"]].map(([path,label])=><div className="px-5 py-4" key={path}><code className="mono text-[12px] text-accent">GET {path}</code><p className="mt-1 text-[12px] text-ink-3">{label}</p></div>)}</div><p className="mt-5 text-[12px] leading-6 text-ink-4">原站的 RSS、API 和 MCP 历史出口也要求有效登录会话。无法发送会话 Cookie 的阅读器或客户端不能直接使用这些出口。</p>
  </section>;
}
