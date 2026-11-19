import { BackLink } from "../components/ui/BackLink";
import type { Screen } from "../components/shell/screens";
import { useState, type FormEvent } from "react";
import { Link, useLoaderData, useRevalidator } from "react-router";
import type { Route } from "./+types/model-settings";
import { fieldClass, FormMessage, labelClass, postJSON, putJSON } from "../features/account/common";
import { userApiGet, userApiMe } from "../lib/user-api.server";
import { useLocalePreference } from "../lib/local-state";
import { getDict } from "../lib/i18n";

export const handle: Screen = { tab: "me", name: "模型配置" };

/** Load the user's current model configuration and CSRF token. */
export async function loader({ request }: Route.LoaderArgs) {
  const [user, model] = await Promise.all([
    userApiMe(request),
    userApiGet<{ configured: boolean; baseUrl: string; model: string; apiKeySet?: boolean }>(request, "/api/me/model"),
  ]);
  return { csrfToken: user?.csrfToken ?? "", ...model };
}

export const meta: Route.MetaFunction = () => [{ title: "模型配置 · MyHOT" }, { name: "robots", content: "noindex, nofollow" }];

export const headers: Route.HeadersFunction = () => ({ "Cache-Control": "private, no-store", "X-Robots-Tag": "noindex, nofollow" });

/** Render the user's OpenAI-compatible model configuration and connectivity test form. */
export default function ModelSettings() {
  const initial = useLoaderData<typeof loader>();
  const revalidator = useRevalidator();
  const locale = useLocalePreference();
  const dict = getDict(locale);
  const isEn = locale === "en";

  const [baseUrl, setBaseUrl] = useState(initial.baseUrl);
  const [model, setModel] = useState(initial.model);
  const [apiKey, setApiKey] = useState("");
  const [pendingTest, setPendingTest] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  /** Save the user's model endpoint, model identifier, and optional API key. */
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError("");
    setMessage("");
    try {
      await putJSON("/api/me/model", { baseUrl, model, apiKey }, initial.csrfToken);
      setApiKey("");
      setPendingTest(null);
      void revalidator.revalidate();
      setMessage(dict.model.savedSuccess);
    } catch (e) {
      setError(e instanceof Error ? e.message : isEn ? "Save failed" : "保存失败");
    } finally {
      setBusy(false);
    }
  }

  /** Queue and poll a model connectivity test using the form values. */
  async function test() {
    setBusy(true);
    setError("");
    setMessage("");
    try {
      const testId = pendingTest ?? (await postJSON<{ testId: number }>("/api/me/model/test", { baseUrl, model, apiKey }, initial.csrfToken)).testId;
      setPendingTest(testId);
      setMessage(isEn ? "Connectivity test queued, waiting for result…" : "连接测试已排队，正在等待结果…");
      const deadline = Date.now() + 70_000;
      while (Date.now() < deadline) {
        await new Promise((resolve) => setTimeout(resolve, 2000));
        const remaining = deadline - Date.now();
        if (remaining <= 0) {
          break;
        }
        const response = await fetch(`/api/me/model/test/${testId}`, {
          credentials: "same-origin",
          cache: "no-store",
          signal: AbortSignal.timeout(Math.min(5000, remaining)),
        });
        const result = (await response.json()) as {
          status: string;
          usage?: { prompt_tokens: number; completion_tokens: number };
          error?: string;
        };
        if (!response.ok) {
          throw new Error(result.error || (isEn ? "Failed to read test result" : "无法读取测试结果"));
        }
        if (result.status === "failed") {
          setPendingTest(null);
          throw new Error(result.error || (isEn ? "Connection test failed" : "连接测试失败"));
        }
        if (result.status === "completed") {
          setPendingTest(null);
          setMessage(
            isEn
              ? `Connection successful · ${result.usage?.prompt_tokens ?? 0} prompt tokens, ${result.usage?.completion_tokens ?? 0} completion tokens`
              : `连接成功 · 输入 ${result.usage?.prompt_tokens ?? 0} tokens，输出 ${result.usage?.completion_tokens ?? 0} tokens`
          );
          return;
        }
      }
      setMessage(isEn ? "Test still running in background, click to check result later." : "测试仍在后台处理中，稍后点击查看测试结果。");
    } catch (e) {
      setMessage("");
      setError(e instanceof Error ? e.message : isEn ? "Connection test failed" : "连接测试失败");
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="mx-auto max-w-[720px] pb-10">
      <BackLink to="/" label={dict.action.backToFeed} />
      <h1 className="mt-3 text-[25px] font-semibold tracking-tight text-ink">{dict.model.title}</h1>
      <p className="mt-2 text-[13px] leading-relaxed text-ink-3">{dict.model.subtitle}</p>
      <form onSubmit={save} className="card mt-6 p-5 sm:p-6">
        <label className={labelClass} htmlFor="baseUrl">
          {dict.model.baseUrl}
        </label>
        <input
          className={fieldClass}
          id="baseUrl"
          name="baseUrl"
          type="url"
          autoComplete="url"
          placeholder="https://api.example.com/v1"
          value={baseUrl}
          onChange={(e) => setBaseUrl(e.target.value)}
          required
        />
        <label className={labelClass} htmlFor="model">
          {dict.model.modelName}
        </label>
        <input
          className={fieldClass}
          id="model"
          name="model"
          value={model}
          onChange={(e) => setModel(e.target.value)}
          placeholder="gpt-4o-mini"
          maxLength={160}
          required
        />
        <label className={labelClass} htmlFor="apiKey">
          {dict.model.apiKey} {initial.apiKeySet && <span className="font-normal text-ink-4">{dict.model.apiKeySavedHint}</span>}
        </label>
        <input
          className={fieldClass}
          id="apiKey"
          name="apiKey"
          type="password"
          autoComplete="new-password"
          value={apiKey}
          onChange={(e) => setApiKey(e.target.value)}
          placeholder={initial.apiKeySet ? "••••••••••••••••" : dict.model.apiKeyPlaceholder}
          required={!initial.apiKeySet}
        />
        <p className="mt-3 text-[11.5px] leading-relaxed text-ink-4">{dict.model.notice}</p>
        {error && <FormMessage error>{error}</FormMessage>}
        {message && <FormMessage>{message}</FormMessage>}
        <div className="mt-5 flex flex-wrap gap-2">
          <button
            className="inline-flex h-10 items-center rounded-full bg-accent px-5 text-[13px] font-semibold text-accent-contrast hover:bg-accent-ink disabled:opacity-50"
            disabled={busy}
          >
            {busy ? dict.action.pleaseWait : isEn ? "Save Model" : "保存模型"}
          </button>
          <button
            type="button"
            onClick={test}
            className="inline-flex h-10 items-center rounded-full border border-line-strong px-5 text-[13px] font-medium text-ink-2 hover:bg-bg-sunk disabled:opacity-50"
            disabled={busy || (!apiKey && !initial.apiKeySet)}
          >
            {busy ? dict.action.pleaseWait : pendingTest ? dict.action.viewTestResult : dict.action.testConnection}
          </button>
        </div>
      </form>
    </main>
  );
}
