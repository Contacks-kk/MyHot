// 站点的字标和品牌图标：页面用 Wordmark 画标志与站名（size 是基准尺寸，单位像素），RingMark 是加载环。
import { SITE } from "@aihot/site";

const BRAND_ICON_SRC = "/icon-192.png?v=2";

/** Render the spider mascot brand mark at the requested square pixel size. */
export function BrandIcon({ size = 32, className = "" }: { size?: number; className?: string }) {
  return (
    <img
      src={BRAND_ICON_SRC}
      width={size}
      height={size}
      alt=""
      aria-hidden="true"
      className={`shrink-0 object-contain select-none ${className}`}
    />
  );
}

/** Render the brand icon paired with the site name wordmark. */
export function Wordmark({ size = 24, className = "", title = SITE.name }: { size?: number; className?: string; title?: string }) {
  const iconSize = Math.round(size * 1.22);
  const fontSize = Math.round(size * 0.82);

  return (
    <span
      className={`inline-flex items-center gap-[0.42em] font-black leading-none tracking-[-0.03em] ${className}`}
      style={{ fontSize }}
      aria-label={title}
      role="img"
    >
      <BrandIcon size={iconSize} />
      <span aria-hidden="true">{SITE.name}</span>
    </span>
  );
}

/** A ring with a dot; spinning, it is the loader. */
export function RingMark({ className = "", spinning = false }: { className?: string; spinning?: boolean }) {
  return (
    <svg viewBox="0 0 24 24" className={className} aria-hidden="true">
      <g style={spinning ? { transformOrigin: "12px 12px", animation: "spin-slow 1.1s linear infinite" } : undefined}>
        <circle cx="12" cy="12" r="9" fill="none" stroke="currentColor" strokeWidth="2.6" strokeLinecap="round" strokeDasharray="42 15" />
      </g>
      <circle cx="12" cy="12" r="2.6" fill="currentColor" />
    </svg>
  );
}
