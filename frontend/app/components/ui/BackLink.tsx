import type { MouseEvent, ReactNode } from "react";
import { Link, useNavigate } from "react-router";
import { IconChevronLeft } from "../icons";
import { markBack } from "../shell/transitions";
import { historyIndex } from "../shell/screens";

/** Standard back navigation link styled with the signature chevron and accent tone. */
export function BackLink({
  to = "/",
  label,
  children,
  className = "",
}: {
  to?: string;
  label?: ReactNode;
  children?: ReactNode;
  className?: string;
}) {
  const navigate = useNavigate();
  const content = children ?? label;

  /** Handle back click with viewTransition and history awareness. */
  function handleClick(event: MouseEvent<HTMLAnchorElement>) {
    // Check for modifier keys or non-primary mouse button
    if (event.button !== 0 || event.metaKey || event.altKey || event.ctrlKey || event.shiftKey) {
      return;
    }

    event.preventDefault();
    markBack();

    // Navigate back in history if available; otherwise use fallback route
    if (typeof window !== "undefined" && (window.history.state?.idx > 0 || historyIndex() > 0)) {
      navigate(-1);
    } else {
      navigate(to, { viewTransition: true });
    }
  }

  return (
    <Link
      to={to}
      onClick={handleClick}
      className={`group -ml-1 inline-flex h-11 items-center gap-0.5 text-[16px] font-medium text-accent transition-opacity active:opacity-60 hover:opacity-85 ${className}`}
    >
      <IconChevronLeft size={24} strokeWidth={2.1} className="shrink-0 transition-transform group-hover:-translate-x-0.5" />
      <span className="truncate">{content}</span>
    </Link>
  );
}
