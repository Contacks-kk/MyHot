import { useId, useRef } from "react";
import { createPortal } from "react-dom";
import { IconCheck, IconClose } from "../../components/icons";
import { useModal } from "../../components/ui/modal";

export function VerificationNotice({ result, onClose }: {
  result: "success" | "error" | null;
  onClose: () => void;
}) {
  const panel = useRef<HTMLDivElement>(null);
  const titleId = useId();
  useModal({ open: result !== null, panel, onClose });
  if (!result) return null;
  const success = result === "success";
  return createPortal(
    <div className="fixed inset-0 z-[100] flex items-center justify-center bg-black/35 px-5" onClick={onClose}>
      <div ref={panel} role="dialog" aria-modal="true" aria-labelledby={titleId} tabIndex={-1}
        className="w-full max-w-[320px] rounded-card border border-line bg-surface p-6 text-center shadow-xl outline-none"
        onClick={event => event.stopPropagation()}>
        <div className={`mx-auto flex size-14 items-center justify-center rounded-full ${success ? "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400" : "bg-red-500/10 text-red-600 dark:text-red-400"}`}>
          {success ? <IconCheck size={30} strokeWidth={2.5}/> : <IconClose size={30} strokeWidth={2.5}/>}
        </div>
        <h2 id={titleId} className="mt-4 text-[18px] font-semibold text-ink">{success ? "验证码正确" : "验证码错误"}</h2>
        <button type="button" onClick={onClose} className="mt-5 h-10 w-full rounded-full bg-accent text-[13px] font-semibold text-accent-contrast hover:bg-accent-ink">知道了</button>
      </div>
    </div>, document.body,
  );
}
