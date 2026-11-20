import { LogoutDialog } from "../components/shell/LogoutDialog";
import { useState, type FormEvent } from "react";
import { useLoaderData, Link } from "react-router";
import type { Route } from "./+types/more";
import type { Screen } from "../components/shell/screens";
import { deleteJSON, fieldClass, FormMessage, labelClass, postJSON } from "../features/account/common";
import { IconLogout } from "../components/icons";
import { userApiGet, type UserSession } from "../lib/user-api.server";
import { useLocalePreference } from "../lib/local-state";
import { getDict } from "../lib/i18n";
import { ThemeSwitch } from "../components/shell/ThemeSwitch";

export const handle: Screen = {
  tab: "me",
  name: "我的账号",
};

/** Disable caching for user account settings pages. */
export const headers: Route.HeadersFunction = () => ({
  "Cache-Control": "private, no-store",
});

/** Load the authenticated user's session details including email and CSRF token. */
export const loader = ({ request }: Route.LoaderArgs) => userApiGet<UserSession>(request, "/api/me");

/** Render the account overview, policy links, and account deletion form. */
export default function MorePage() {
  const user = useLoaderData<typeof loader>();
  const locale = useLocalePreference();
  const dict = getDict(locale);
  const [logoutConfirmOpen, setLogoutConfirmOpen] = useState(false);
  const [logoutBusy, setLogoutBusy] = useState(false);

  /** Submit user logout request and redirect to home. */
  async function handleLogout() {
    setLogoutBusy(true);

    // Submit logout request to backend
    try {
      await postJSON("/api/auth/logout", {}, user.csrfToken);
      window.location.assign("/");
    } catch {
      window.location.assign("/");
    } finally {
      setLogoutBusy(false);
    }
  }

  return (
    <section className="mx-auto max-w-[720px] pb-10">
      <h1 className="text-[25px] font-semibold tracking-tight text-ink">{dict.account.title}</h1>
      <p className="mt-2 text-[13px] text-ink-4">{user.email}</p>
      <nav aria-label={locale === "en" ? "Workspace settings" : "工作区设置"} className="card mt-6 divide-y divide-line text-[14px] text-ink-2 lg:hidden">
        {[
          ["/model", dict.nav.model],
          ["/sources", dict.nav.sources],
          ["/history", dict.nav.history],
          ["/about", dict.nav.about],
        ].map(([to, label]) => (
          <Link key={to} to={to!} className="flex items-center justify-between px-5 py-4 hover:bg-bg-sunk">
            <span>{label}</span>
            <span aria-hidden>›</span>
          </Link>
        ))}
      </nav>
      <div className="card mt-5 flex items-center justify-between px-5 py-4 lg:hidden">
        <span className="text-[14px] font-medium text-ink">{dict.shell.appearance}</span>
        <ThemeSwitch className="w-[112px]" />
      </div>
      <div className="card mt-6 divide-y divide-line text-[14px] text-ink-2">
        {[
          ["/terms", dict.account.terms],
          ["/privacy", dict.account.privacy],
        ].map(([to, label]) => (
          <Link key={to} to={to!} className="flex items-center justify-between px-5 py-4 hover:bg-bg-sunk">
            <span>{label}</span>
            <span aria-hidden>›</span>
          </Link>
        ))}
      </div>

      <div className="mt-5">
        <button
          type="button"
          onClick={() => setLogoutConfirmOpen(true)}
          className="flex h-11 w-full items-center justify-center gap-2 rounded-control border border-line bg-surface text-[14px] font-medium text-ink-2 shadow-sm transition-colors hover:bg-bg-sunk hover:text-hot"
        >
          <IconLogout size={16} />
          <span>{dict.action.logout}</span>
        </button>
      </div>

      <LogoutDialog
        open={logoutConfirmOpen}
        onClose={() => setLogoutConfirmOpen(false)}
        onConfirm={handleLogout}
        busy={logoutBusy}
        locale={locale}
      />
      <section className="mt-8 rounded-card border border-line bg-surface p-5">
        <h2 className="text-[15px] font-semibold text-ink">{dict.action.deleteAccount}</h2>
        <p className="mt-1.5 text-[12px] leading-relaxed text-ink-4">
          {dict.account.deleteNotice}
        </p>
        <DeleteAccount csrfToken={user.csrfToken} />
      </section>
    </section>
  );
}

/** Render the password-confirmed permanent account deletion form. */
function DeleteAccount({ csrfToken }: { csrfToken: string }) {
  const locale = useLocalePreference();
  const dict = getDict(locale);
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  /** Submit account deletion after explicit confirmation and redirect to home. */
  async function remove(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!confirm) {
      return;
    }

    setBusy(true);
    setError("");
    try {
      await deleteJSON("/api/me", { password }, csrfToken);
      window.location.assign("/");
    } catch (e) {
      setError(e instanceof Error ? e.message : "删除失败");
    } finally {
      setBusy(false);
    }
  }

  return (
    <form method="post" onSubmit={remove} className="mt-3">
      <label className={labelClass} htmlFor="confirmPassword">
        {dict.action.enterPasswordConfirm}
      </label>
      <input
        className={fieldClass}
        id="confirmPassword"
        type="password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        required
        autoComplete="current-password"
      />
      <label className="mt-3 flex items-start gap-2 text-[12px] text-ink-3">
        <input
          type="checkbox"
          className="mt-0.5 accent-hot"
          checked={confirm}
          onChange={(e) => setConfirm(e.target.checked)}
        />
        {dict.action.confirmUnderstand}
      </label>
      {error && <FormMessage error>{error}</FormMessage>}
      <button
        disabled={busy || !confirm}
        className="mt-4 h-9 rounded-full border border-hot/40 px-4 text-[12px] font-medium text-hot hover:bg-hot/5 disabled:opacity-45"
      >
        {busy ? dict.action.deleting : dict.action.permanentlyDelete}
      </button>
    </form>
  );
}
