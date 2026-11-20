import { useState, type FormEvent } from "react";
import { Link, useLoaderData } from "react-router";
import type { Route } from "./+types/interest-labels";
import { adminGet } from "../../lib/admin.server";
import { useAdminAction } from "../../features/admin/action";
import { AdminPage, Card, Input, Select } from "../../features/admin/ui";

interface Label {
  id: string;
  name: string;
  aliases: string[];
  definition: string;
  kind: "entity" | "topic";
  status: keyof typeof statusNames;
  articles: number;
  publishers: number;
  weeks: number;
  latest?: string | null;
}

interface Evidence {
  id: string;
  title: string;
  excerpt: string;
}

const statusNames = {
  active: "已晋升",
  candidate: "候选",
  dormant: "休眠",
  archived: "已归档",
  merged: "已合并",
  withdrawn: "已撤下",
};

/** Load label lifecycle data and the selected shared article evidence. */
export async function loader({ request }: Route.LoaderArgs) {
  const id = new URL(request.url).searchParams.get("label") ?? "";
  const [catalog, evidence] = await Promise.all([
    adminGet<{ labels: Label[] }>(request, "/api/admin/interest-labels"),
    id ? adminGet<{ articles: Evidence[] }>(request, `/api/admin/interest-labels/${encodeURIComponent(id)}/articles`) : null,
  ]);
  return { labels: catalog.labels, id, articles: evidence?.articles ?? [] };
}

/** Render moderation actions for the selected label. */
function Curate({ label, labels }: { label: Label; labels: Label[] }) {
  const action = useAdminAction();
  const [target, setTarget] = useState("");
  const [reason, setReason] = useState("");

  /** Submit an auditable merge or withdrawal. */
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const result = await action.run("POST", `/api/admin/interest-labels/${encodeURIComponent(label.id)}`, {
      action: target ? "merge" : "withdraw",
      targetId: target,
      reason,
    }, { success: target ? "标签已合并" : "标签已撤下" });
    if (result) {
      setReason("");
    }
  }

  return (
    <form className="mt-4 flex flex-wrap items-end gap-2" onSubmit={save}>
      <label className="text-[12px]">操作
        <Select value={target} onChange={event => setTarget(event.target.value)}>
          <option value="">撤下标签</option>
          {labels.filter(item => item.id !== label.id && item.kind === label.kind && item.status === "active")
            .map(item => <option key={item.id} value={item.id}>合并到 {item.name}</option>)}
        </Select>
      </label>
      <label className="text-[12px]">原因
        <Input value={reason} onChange={event => setReason(event.target.value)} required maxLength={300} />
      </label>
      <button type="submit" disabled={action.busy || !reason.trim()} className="rounded-control border border-line px-3 py-2 text-[12px] disabled:opacity-50">保存操作</button>
    </form>
  );
}

/** Show the unified label catalog, lifecycle and its article evidence. */
export default function InterestLabelsAdmin() {
  const { labels, id, articles } = useLoaderData<typeof loader>();
  const [status, setStatus] = useState("");
  const [search, setSearch] = useState("");
  const selected = labels.find(label => label.id === id);
  const term = search.trim().toLowerCase();
  const visible = labels.filter(label => (!status || label.status === status) &&
    [label.name, ...label.aliases].some(name => name.toLowerCase().includes(term)));

  return (
    <AdminPage title="标签管理" subtitle="查看关注标签的晋升状态与共享信源文章证据。">
      <Link className="text-[13px] text-accent" to="/admin/labels/history">查看旧站公开发布标签统计</Link>
      <div className="my-4 flex flex-wrap gap-2">
        <Select aria-label="标签状态" value={status} onChange={event => setStatus(event.target.value)}>
          <option value="">全部状态</option>
          {Object.entries(statusNames).map(([key, name]) => <option key={key} value={key}>{name}</option>)}
        </Select>
        <Input aria-label="搜索标签" placeholder="搜索名称或别名" value={search} onChange={event => setSearch(event.target.value)} />
      </div>
      <Card title="标签目录">
        <div className="overflow-x-auto">
          <table className="w-full text-left text-[13px]">
            <thead><tr><th>标签</th><th>状态</th><th>证据文章</th><th>独立发布方</th><th>最近命中</th></tr></thead>
            <tbody className="divide-y divide-line">
              {visible.map(label => (
                <tr key={label.id}>
                  <td className="min-w-32 py-2 pr-3"><Link to={`?label=${encodeURIComponent(label.id)}`} className="font-medium text-accent">{label.name}</Link></td>
                  <td>{statusNames[label.status]}</td>
                  <td>{label.articles}</td>
                  <td>{label.publishers}</td>
                  <td>{label.latest?.slice(0, 10) ?? "暂无"}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {visible.length === 0 && <p className="py-4 text-[13px] text-ink-4">没有符合条件的标签。</p>}
        </div>
      </Card>
      {selected && (
        <Card title={selected.name}>
          <p className="text-[13px] text-ink-3">{selected.definition}</p>
          {selected.status !== "merged" && selected.status !== "withdrawn" && <Curate key={selected.id} label={selected} labels={labels} />}
          <ul className="mt-4 divide-y divide-line">
            {articles.map(article => (
              <li key={article.id} className="py-2 text-[13px]">
                <p className="font-medium">{article.title}</p>
                <p className="text-ink-3">{article.excerpt}</p>
              </li>
            ))}
          </ul>
          {articles.length === 0 && <p className="mt-4 text-[13px] text-ink-4">暂无关联证据。</p>}
        </Card>
      )}
    </AdminPage>
  );
}
