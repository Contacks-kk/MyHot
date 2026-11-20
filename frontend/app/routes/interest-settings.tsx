import { useState, type FormEvent } from "react";
import { useLoaderData } from "react-router";
import { BackLink } from "../components/ui/BackLink";
import type { Screen } from "../components/shell/screens";
import type { Route } from "./+types/interest-settings";
import { FormMessage, fieldClass } from "../features/account/common";
import { userApiGet, userApiMe } from "../lib/user-api.server";

export const handle: Screen = { tab: "interests", name: "关注项" };

interface Label {
  id: string;
  name: string;
  kind: "entity" | "topic";
  definition: string;
  aliases: string[];
  status: "active" | "dormant";
  count: number | null;
}

/** Load the user's saved preferences and subscription-scoped label directory. */
export async function loader({ request }: Route.LoaderArgs) {
  const [user, interests, catalog] = await Promise.all([
    userApiMe(request),
    userApiGet<{ keywords: string[]; entityIds: string[]; labelIds: string[] }>(request, "/api/me/interests"),
    userApiGet<{ labels: Label[] }>(request, "/api/me/labels"),
  ]);
  return { csrfToken: user?.csrfToken ?? "", ...interests, labels: catalog.labels };
}

export const meta: Route.MetaFunction = () => [{ title: "关注项 · MyHOT" }, { name: "robots", content: "noindex, nofollow" }];
export const headers: Route.HeadersFunction = () => ({ "Cache-Control": "private, no-store", "X-Robots-Tag": "noindex, nofollow" });

/** Render selected preferences and searchable stable label choices. */
export default function InterestSettings() {
  const initial = useLoaderData<typeof loader>();
  const [keywords, setKeywords] = useState(initial.keywords.join("\n"));
  const [selected, setSelected] = useState(() => new Set([...initial.entityIds, ...initial.labelIds]));
  const [search, setSearch] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const entityIDs = new Set(initial.labels.filter(label => label.kind === "entity").map(label => label.id));
  const recommendations = initial.labels
    .filter(label => label.status === "active" && (label.count ?? 0) > 0 && !selected.has(label.id))
    .sort((a, b) => (b.count ?? 0) - (a.count ?? 0) || a.name.localeCompare(b.name))
    .slice(0, 6);
  const term = search.toLowerCase().trim();
  const visible = initial.labels.filter(label => (label.status === "active" || selected.has(label.id))
    && [label.name, ...label.aliases].some(name => name.toLowerCase().includes(term)));

  /** Toggle a label in the pending form selection. */
  function toggle(id: string) {
    setSelected(values => {
      const next = new Set(values);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  }

  /** Save labels together with the existing keyword and entity preferences. */
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError("");
    setMessage("");
    try {
      const response = await fetch("/api/me/interests", {
        method: "PUT",
        headers: { "Content-Type": "application/json", "X-CSRF-Token": initial.csrfToken },
        body: JSON.stringify({
          keywords: keywords.split(/[\n,，]/),
          entityIds: [...selected].filter(id => entityIDs.has(id) || initial.entityIds.includes(id)),
          labelIds: [...selected].filter(id => !entityIDs.has(id) && !initial.entityIds.includes(id)),
        }),
      });
      const result = await response.json() as { error?: string };
      if (!response.ok) {
        throw new Error(result.error || "保存失败");
      }
      setMessage("关注项已保存，新设置从后续采集文章开始生效。");
    } catch (err) {
      setError(err instanceof Error ? err.message : "保存失败");
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="mx-auto max-w-[760px] pb-10">
      <BackLink to="/hot" label="个人热点榜单" />
      <h1 className="mt-3 text-[25px] font-semibold tracking-tight text-ink">关注项</h1>
      <p className="mt-2 text-[13px] text-ink-3">新文章命中任一关键词、公司或技术主题后，会进入个人筛选。</p>
      <form onSubmit={save} className="card mt-6 space-y-6 p-5 sm:p-6">
        <div>
          <label htmlFor="keywords" className="text-[13px] font-semibold text-ink">关键词</label>
          <p className="mt-1 text-[11.5px] text-ink-4">每行一个，也可用逗号分隔，最多 30 个。</p>
          <textarea id="keywords" value={keywords} onChange={event => setKeywords(event.target.value)} className={fieldClass + " mt-2 min-h-24 resize-y py-3"} maxLength={4000} />
        </div>
        <div>
          <label htmlFor="label-search" className="text-[13px] font-semibold text-ink">搜索稳定标签</label>
          <input id="label-search" type="search" value={search} onChange={event => setSearch(event.target.value)} className={fieldClass + " mt-2"} />
          <p className="mt-2 text-[11.5px] text-ink-4">文章数统计本人启用的信源近 7 天的去重文章。</p>
        </div>
        {!term && recommendations.length > 0 && (
          <section>
            <h2 className="text-[13px] font-semibold text-ink">来自我的信源</h2>
            <div className="mt-2 flex flex-wrap gap-2">
              {recommendations.map(label => (
                <button key={label.id} type="button" onClick={() => toggle(label.id)} className="rounded-full border border-line px-3 py-2 text-[12px] text-accent">
                  ＋ {label.name} · {label.count} 篇
                </button>
              ))}
            </div>
          </section>
        )}
        {(["entity", "topic"] as const).map(kind => (
          <fieldset key={kind}>
            <legend className="text-[13px] font-semibold text-ink">{kind === "entity" ? "公司与产品" : "技术主题"}</legend>
            <div className="mt-2 grid gap-2 sm:grid-cols-2">
              {visible.filter(label => label.kind === kind).map(label => (
                <label key={label.id} className={"flex cursor-pointer items-start gap-2 rounded-control border p-3 text-[13px] " +
                  (selected.has(label.id) ? "border-accent/40 bg-accent-soft" : "border-line bg-surface")}>
                  <input type="checkbox" checked={selected.has(label.id)} onChange={() => toggle(label.id)} className="mt-1 accent-accent" />
                  <span>
                    <strong className="font-medium">{label.name}</strong>
                    <span className="block text-[11px] text-ink-4">{label.status === "dormant" ? "已休眠 · " : ""}{label.count == null ? "暂无统计" : `${label.count} 篇`}</span>
                    <span className="block text-[11px] text-ink-3">{label.definition}</span>
                  </span>
                </label>
              ))}
            </div>
          </fieldset>
        ))}
        {visible.length === 0 && <p className="text-[13px] text-ink-4">没有符合条件的稳定标签。</p>}
        {error && <FormMessage error>{error}</FormMessage>}
        {message && <FormMessage>{message}</FormMessage>}
        <button disabled={busy} className="h-10 rounded-full bg-accent px-5 text-[13px] font-semibold text-accent-contrast disabled:opacity-50">
          {busy ? "正在保存…" : "保存关注项"}
        </button>
      </form>
    </section>
  );
}
