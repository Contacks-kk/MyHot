import { useLoaderData, Link } from "react-router";
import type { Route } from "./+types/more";
import type { Screen } from "../components/shell/screens";
import { ThemeSwitch } from "../components/shell/ThemeSwitch";
import { userApiGet } from "../lib/user-api.server";
export const handle: Screen = { tab: "me", name: "我的" };
export const headers: Route.HeadersFunction = () => ({ "Cache-Control": "private, no-store" });
export const loader = ({ request }: Route.LoaderArgs) => userApiGet<{email:string}>(request,"/api/me");
export default function MorePage() {
  const user = useLoaderData<typeof loader>();
  return <section className="mx-auto max-w-[720px] pb-10"><h1 className="text-[25px] font-semibold tracking-tight text-ink">我的账号</h1><p className="mt-2 text-[13px] text-ink-4">{user.email}</p>
    <div className="card mt-6 divide-y divide-line text-[14px] text-ink-2">{[["/model","模型配置与账号管理"],["/interests","关注的关键词和公司"],["/sources","我的信息源"],["/history","原站历史归档"],["/about","关于 MyHOT"],["/terms","使用规则"],["/privacy","隐私说明"]].map(([to,label])=><Link key={to} to={to!} className="flex items-center justify-between px-5 py-4 hover:bg-bg-sunk"><span>{label}</span><span aria-hidden>›</span></Link>)}<div className="flex items-center justify-between px-5 py-4"><span>外观</span><ThemeSwitch className="w-[126px]"/></div></div>
  </section>;
}
