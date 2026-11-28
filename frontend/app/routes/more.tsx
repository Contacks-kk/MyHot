import { LogoutDialog } from "../components/shell/LogoutDialog";
import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { useLoaderData, Link, useSearchParams } from "react-router";
import type { Route } from "./+types/more";
import type { Screen } from "../components/shell/screens";
import { deleteJSON, fieldClass, FormMessage, labelClass, postJSON } from "../features/account/common";
import { IconClose, IconLogout, IconMapPin } from "../components/icons";
import { userApiGet, type UserSession } from "../lib/user-api.server";
import { useLocalePreference, useWeatherLocationPreference } from "../lib/local-state";
import { getDict } from "../lib/i18n";
import { ThemeSwitch } from "../components/shell/ThemeSwitch";
import { LocaleSwitch } from "../components/shell/LocaleSwitch";
import { useModal } from "../components/ui/modal";
import { WeatherLocationDialog } from "../features/weather/WeatherLocationDialog";

export const handle: Screen = {
  tab: "me",
  name: getDict("zh").nav.account,
};

/** Disable caching for user account settings pages. */
export const headers: Route.HeadersFunction = () => ({
  "Cache-Control": "private, no-store",
});

/** Load the authenticated user's session details including email and CSRF token. */
export const loader = ({ request }: Route.LoaderArgs) => userApiGet<UserSession>(request, "/api/me");

/** Render the settings overview, language/appearance switches, policy options, logout, and delete dialog. */
export default function MorePage() {
  const user = useLoaderData<typeof loader>();
  const locale = useLocalePreference();
  const weatherLocation = useWeatherLocationPreference();
  const dict = getDict(locale);
  const [searchParams, setSearchParams] = useSearchParams();
  const [logoutConfirmOpen, setLogoutConfirmOpen] = useState(false);
  const [logoutBusy, setLogoutBusy] = useState(false);
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false);
  const [locationDialogOpen, setLocationDialogOpen] = useState(false);

  // Open the city selector modal automatically when arriving via /daily Modify Location link
  useEffect(() => {
    if (searchParams.get("location") === "edit") {
      setLocationDialogOpen(true);
    }
  }, [searchParams]);

  /** Close the city selector modal and remove transient query parameter if present. */
  function handleCloseLocationDialog() {
    setLocationDialogOpen(false);
    if (searchParams.has("location")) {
      const next = new URLSearchParams(searchParams);
      next.delete("location");
      setSearchParams(next, { replace: true });
    }
  }

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

      <nav
        aria-label={locale === "en" ? "Workspace settings" : "工作区设置"}
        className="card mt-6 divide-y divide-line overflow-hidden text-[14px] text-ink-2 lg:hidden"
      >
        {[
          ["/model", dict.nav.model],
          ["/sources", dict.nav.sources],
          ["/interests", dict.nav.interests],
          ["/history", dict.nav.history],
          ["/about", dict.nav.about],
        ].map(([to, label]) => (
          <Link key={to} to={to!} className="flex items-center justify-between px-5 py-4 transition-colors hover:bg-bg-sunk">
            <span>{label}</span>
            <span aria-hidden>›</span>
          </Link>
        ))}
      </nav>

      <div className="card mt-5 divide-y divide-line overflow-hidden text-[14px] text-ink-2 lg:mt-6">
        <div className="flex items-center justify-between px-5 py-3.5">
          <span className="font-medium text-ink">{dict.shell.language}</span>
          <LocaleSwitch className="w-[112px]" />
        </div>
        <div className="flex items-center justify-between gap-4 px-5 py-3.5">
          <div className="min-w-0">
            <div className="font-medium text-ink">{dict.shell.weatherLocation}</div>
            <div className="mt-0.5 truncate text-[12px] text-ink-4">
              {weatherLocation ? weatherLocation.label : dict.shell.weatherLocationAuto}
            </div>
          </div>
          <button
            type="button"
            onClick={() => setLocationDialogOpen(true)}
            className="inline-flex shrink-0 items-center gap-1.5 rounded-full border border-line-strong bg-bg-sunk px-3 py-1.5 text-[12px] font-semibold text-ink transition-colors hover:border-accent hover:text-accent"
          >
            <IconMapPin size={13} />
            <span>{dict.shell.changeLocation}</span>
          </button>
        </div>
        <div className="flex items-center justify-between px-5 py-3.5 lg:hidden">
          <span className="font-medium text-ink">{dict.shell.appearance}</span>
          <ThemeSwitch className="w-[112px]" />
        </div>
      </div>

      <div className="card mt-5 divide-y divide-line overflow-hidden text-[14px] text-ink-2 lg:mt-6">
        {[
          ["/terms", dict.account.terms],
          ["/privacy", dict.account.privacy],
        ].map(([to, label]) => (
          <Link key={to} to={to!} className="flex items-center justify-between px-5 py-4 transition-colors hover:bg-bg-sunk">
            <span>{label}</span>
            <span aria-hidden>›</span>
          </Link>
        ))}
        <button
          type="button"
          onClick={() => setDeleteDialogOpen(true)}
          className="flex w-full items-center justify-between px-5 py-4 text-left transition-colors hover:bg-bg-sunk hover:text-hot"
        >
          <span>{dict.action.deleteAccount}</span>
          <span aria-hidden>›</span>
        </button>
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
        <LogoutDialog
          open={logoutConfirmOpen}
          onClose={() => setLogoutConfirmOpen(false)}
          onConfirm={handleLogout}
          busy={logoutBusy}
          locale={locale}
        />
        <DeleteAccountDialog
          open={deleteDialogOpen}
          onClose={() => setDeleteDialogOpen(false)}
          csrfToken={user.csrfToken}
        />
        <WeatherLocationDialog
          open={locationDialogOpen}
          onClose={handleCloseLocationDialog}
          language={locale}
          currentLocation={weatherLocation}
        />
      </div>
    </section>
  );
}

/** Modal dialog requesting password and explicit confirmation before permanent account deletion. */
function DeleteAccountDialog({
  open,
  onClose,
  csrfToken,
}: {
  open: boolean;
  onClose: () => void;
  csrfToken: string;
}) {
  const locale = useLocalePreference();
  const dict = getDict(locale);
  const isEn = locale === "en";
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const panelRef = useRef<HTMLDivElement>(null);
  const passwordRef = useRef<HTMLInputElement>(null);
  const titleId = useId();

  /** Guard modal dismissal while deletion request is in flight. */
  const handleClose = () => {
    if (busy) {
      return;
    }

    onClose();
  };

  useModal({
    open,
    panel: panelRef,
    onClose: handleClose,
    initialFocus: passwordRef,
  });

  // Reset sensitive form inputs and error feedback whenever the modal closes
  useEffect(() => {
    if (!open) {
      setPassword("");
      setConfirm(false);
      setError("");
    }
  }, [open]);

  if (!open) {
    return null;
  }

  /** Submit account deletion after explicit confirmation and redirect to home. */
  async function remove(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!confirm) {
      return;
    }

    setBusy(true);
    setError("");

    // Submit permanent deletion request with current password and CSRF token
    try {
      await deleteJSON("/api/me", { password }, csrfToken);
      window.location.assign("/");
    } catch (e) {
      setError(e instanceof Error ? e.message : (isEn ? "Deletion failed" : "删除失败"));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4 backdrop-blur-xs"
      onClick={(e) => {
        if (e.target === e.currentTarget) {
          handleClose();
        }
      }}
    >
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        className="card w-full max-w-[420px] p-5 shadow-xl outline-none sm:p-6"
      >
        <div className="flex items-start justify-between gap-3">
          <div>
            <h2 id={titleId} className="text-[16px] font-semibold text-ink">
              {dict.action.deleteAccount}
            </h2>
            <p className="mt-1.5 text-[12.5px] leading-relaxed text-ink-3">
              {dict.account.deleteNotice}
            </p>
          </div>
          <button
            type="button"
            disabled={busy}
            onClick={handleClose}
            aria-label={isEn ? "Close" : "关闭"}
            className="grid size-8 shrink-0 place-items-center rounded-full text-ink-4 transition-colors hover:bg-bg-sunk hover:text-ink disabled:opacity-45"
          >
            <IconClose size={16} />
          </button>
        </div>

        <form method="post" onSubmit={remove} className="mt-4">
          <label className={labelClass} htmlFor="confirmPassword">
            {dict.action.enterPasswordConfirm}
          </label>
          <input
            ref={passwordRef}
            className={fieldClass}
            id="confirmPassword"
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
            autoComplete="current-password"
          />
          <label className="mt-3.5 flex items-start gap-2 text-[12.5px] text-ink-3">
            <input
              type="checkbox"
              className="mt-0.5 accent-hot"
              checked={confirm}
              onChange={(e) => setConfirm(e.target.checked)}
            />
            <span>{dict.action.confirmUnderstand}</span>
          </label>
          {error && <FormMessage error>{error}</FormMessage>}
          <div className="mt-6 flex justify-end gap-3">
            <button
              type="button"
              disabled={busy}
              onClick={handleClose}
              className="rounded-full border border-line px-4 py-2 text-[13px] font-medium text-ink-2 transition-colors hover:bg-bg-sunk disabled:opacity-45"
            >
              {isEn ? "Cancel" : "取消"}
            </button>
            <button
              type="submit"
              disabled={busy || !confirm}
              className="rounded-full border border-hot/40 bg-hot-soft px-4 py-2 text-[13px] font-semibold text-hot transition-colors hover:border-hot/60 hover:bg-hot/20 disabled:cursor-not-allowed disabled:opacity-45"
            >
              {busy ? dict.action.deleting : dict.action.permanentlyDelete}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
