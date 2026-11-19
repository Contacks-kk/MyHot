import { IconLogout } from "../icons";

/** Modal dialog requesting user confirmation before logging out. */
export function LogoutDialog({
  open,
  onClose,
  onConfirm,
  busy = false,
  locale = "zh",
}: {
  open: boolean;
  onClose: () => void;
  onConfirm: () => void;
  busy?: boolean;
  locale?: "zh" | "en";
}) {
  if (!open) {
    return null;
  }

  const isEn = locale === "en";

  return (
    <div
      role="dialog"
      aria-modal="true"
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4 backdrop-blur-xs"
      onClick={(e) => {
        if (e.target === e.currentTarget && !busy) {
          onClose();
        }
      }}
    >
      <div className="card w-full max-w-[360px] p-5 sm:p-6 shadow-xl">
        <div className="flex items-center gap-3">
          <div className="flex size-10 shrink-0 items-center justify-center rounded-full bg-hot/10 text-hot">
            <IconLogout size={20} />
          </div>
          <div>
            <h3 className="text-[16px] font-semibold text-ink">
              {isEn ? "Confirm Log Out" : "确认退出登录"}
            </h3>
            <p className="mt-1 text-[13px] text-ink-3">
              {isEn ? "Are you sure you want to log out of your account?" : "确定要退出当前账号吗？"}
            </p>
          </div>
        </div>

        <div className="mt-6 flex justify-end gap-3">
          <button
            type="button"
            disabled={busy}
            onClick={onClose}
            className="rounded-full border border-line px-4 py-2 text-[13px] font-medium text-ink-2 hover:bg-bg-sunk transition-colors disabled:opacity-50"
          >
            {isEn ? "Cancel" : "取消"}
          </button>
          <button
            type="button"
            disabled={busy}
            onClick={onConfirm}
            className="rounded-full bg-hot px-4 py-2 text-[13px] font-medium text-white hover:opacity-90 transition-opacity disabled:opacity-50"
          >
            {busy ? (isEn ? "Logging out..." : "正在退出…") : (isEn ? "Log Out" : "退出登录")}
          </button>
        </div>
      </div>
    </div>
  );
}
