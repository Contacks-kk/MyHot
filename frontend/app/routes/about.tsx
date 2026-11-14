import { Link, useRouteLoaderData } from "react-router";
import { SITE } from "@aihot/site";
import type { Route } from "./+types/about";
import type { Screen } from "../components/shell/screens";
import type { loader as rootLoader } from "../root";

export const handle: Screen = {
  tab: "me",
  name: "关于 MyHOT",
};

/** Prevent stale caching across authenticated and anonymous renders of the about page. */
export const headers: Route.HeadersFunction = () => ({
  "Cache-Control": "private, no-store",
});

/** Set the page title metadata for the about route. */
export const meta: Route.MetaFunction = () => [{ title: `关于 · ${SITE.name}` }];

/** Render the product overview either inside the workspace shell or as a standalone public page. */
export default function AboutPage() {
  const root = useRouteLoaderData<typeof rootLoader>("root");
  const hasShell = Boolean(root?.user);

  const content = (
    <article className="mx-auto max-w-[760px]">
      {!hasShell && (
        <Link to="/" className="text-[13px] font-medium text-accent">
          ← MyHOT
        </Link>
      )}
      <h1 className={`${hasShell ? "" : "mt-8 "}text-[34px] font-semibold tracking-tight text-ink`}>
        为你关注的变化，留出每天的阅读时间。
      </h1>
      <p className="mt-5 text-[15px] leading-8 text-ink-3">
        添加自己的 RSS / Atom 信息源，选择关键词和公司实体，并接入 OpenAI 兼容模型，生成个人摘要、热点和每日简报。相同 RSS 地址共用采集结果，订阅名称和频率由各账号独立管理。
      </p>
      <div className="mt-8 grid gap-5 sm:grid-cols-3">
        {[
          ["选择关注项", "新文章命中任一关键词或公司实体后，才会进入你的个人加工队列。修改设置不会回填旧文章。"],
          ["连接自己的模型", "模型服务商使用你的 API Key 计费；API Key 加密保存，个人配置和加工结果按账号隔离。"],
          ["查看个人简报", "热点榜展示过去48小时评分最高的10条。每天北京时间08:00，汇总此前24小时的个人结果。"],
        ].map(([title, text]) => (
          <section className="card p-5" key={title}>
            <h2 className="text-[16px] font-semibold text-ink">{title}</h2>
            <p className="mt-3 text-[13px] leading-7 text-ink-3">{text}</p>
          </section>
        ))}
      </div>
      <p className="mt-8 text-[13px] leading-7 text-ink-4">
        旧信源与文章作为只读历史保留；首次抓取导入的文章不会进入个人模型回填。新的个人结果只在登录后的工作区显示。
      </p>
      <div className="mt-6 flex flex-wrap gap-4 text-[13px] text-accent">
        <Link to="/sources">管理我的信源</Link>
        {!hasShell && (
          <>
            <Link to="/login">登录工作区</Link>
            <Link to="/register">查看注册状态</Link>
          </>
        )}
        <Link to="/terms">使用规则</Link>
        <Link to="/privacy">隐私说明</Link>
      </div>
    </article>
  );

  if (hasShell) {
    return <section className="pb-10">{content}</section>;
  }

  return <main className="min-h-dvh bg-bg px-5 py-12">{content}</main>;
}
