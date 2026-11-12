import {
  useEffect,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
  type ReactNode,
} from "react";
import { Link } from "react-router";
import { SITE } from "@aihot/site";
import {
  IconArrowUpRight,
  IconBolt,
  IconCheck,
  IconClock,
  IconDoc,
  IconFilter,
  IconPlug,
  IconRss,
  IconSparkles,
} from "../../components/icons";
import { ThemeSwitch } from "../../components/shell/ThemeSwitch";

interface RipplePoint {
  x: number;
  y: number;
  age: number;
  duration: number;
  maxRadius: number;
  strength: number;
  echo: boolean;
}

interface PreviewItem {
  id: string;
  category: "model" | "opensource" | "industry";
  categoryLabel: string;
  score: number;
  title: string;
  source: string;
  time: string;
  text: string;
  matchHint: string;
}

interface FunnelStage {
  step: string;
  title: string;
  badge: string;
  summary: string;
  detail: string;
  metricLabel: string;
  metricValue: string;
}

const PREVIEW_ITEMS: readonly PreviewItem[] = [
  {
    id: "preview-1",
    category: "model",
    categoryLabel: "模型",
    score: 94,
    title: "主要模型发布新一代推理与长上下文能力",
    source: "官方工程博客",
    time: "07:18",
    text: "只展示与你选择的公司实体与关键词相关的更新，并由你自己的模型提炼中文摘要。",
    matchHint: "命中实体：基础模型 · 关键词：推理能力",
  },
  {
    id: "preview-2",
    category: "opensource",
    categoryLabel: "开源",
    score: 88,
    title: "开源工具链更新本地量化部署与并发调度方案",
    source: "GitHub Releases",
    time: "06:42",
    text: "个人热点按相关度评分排序；每日简报汇总此前 24 小时的个人筛选结果。",
    matchHint: "命中关键词：开源工具 · 本地部署",
  },
  {
    id: "preview-3",
    category: "industry",
    categoryLabel: "研究",
    score: 81,
    title: "研究团队公布低成本批量筛选与评估基准进展",
    source: "技术研究通讯",
    time: "05:55",
    text: "平台维护公共与隔离 RSS 源，你只需设定关注项即可持续追踪重要变化。",
    matchHint: "命中关键词：批量筛选 · 评估基准",
  },
];

const FUNNEL_STAGES: readonly FunnelStage[] = [
  {
    step: "01",
    title: "共享去重采集",
    badge: "一次抓取 · 订阅共享",
    summary: "相同 RSS / Atom 源全站共用调度与条件请求，新增隔离源首次只放行 48 小时内最新 10 篇。",
    detail: "自动按规范化地址去重源池与文章指纹；历史过往文章归入回填库，防止一次导入上百篇打爆个人模型。",
    metricLabel: "原始进入本轮",
    metricValue: "142 篇 → 过滤旧回填后 38 篇",
  },
  {
    step: "02",
    title: "本地 0-Token 规则初筛",
    badge: "零模型消耗",
    summary: "优先在后台用关键词与公司实体词典做本地匹配，未命中的噪声文章直接跳过。",
    detail: "同时命中关键词或行业实体名称/别名才会保留；未设置任何关注项时则交由下一步批量初筛把关。",
    metricLabel: "本地规则筛选",
    metricValue: "38 篇 → 命中 18 篇（0 次调用）",
  },
  {
    step: "03",
    title: "Batch Triage 批量初筛",
    badge: "省 ~80% Token",
    summary: "待加工超过 5 篇时，先将至多 25 篇标题与 180 字摘要打包成 1 次调用选出 Top 5。",
    detail: "替代对几十篇文章逐篇全文精读，在保留高价值更新的同时显著压低个人 API Key 消耗与等待耗时。",
    metricLabel: "批量初筛漏斗",
    metricValue: "18 篇 → 1 次调用精选 Top 5",
  },
  {
    step: "04",
    title: "私人精读与 08:00 日报",
    badge: "账号严格隔离",
    summary: "仅对精选文章调用你的 OpenAI 兼容模型打分写摘要，并在北京时间 08:00 汇成个人日报。",
    detail: "内置单日调用上限（默认 100 次/日）防止突发洪峰；所有评分、热点榜和简报均隔离在个人工作区中。",
    metricLabel: "最终产出结果",
    metricValue: "48h 热点榜 Top 10 · 08:00 简报",
  },
];

/** Read RGB channels from a CSS custom property for canvas rendering. */
function readThemeRGB(variableName: string, fallback: [number, number, number]): [number, number, number] {
  if (typeof window === "undefined") {
    return fallback;
  }

  // Parse rgb()/hex strings produced by computed styles across light and dark themes.
  const raw = getComputedStyle(document.documentElement).getPropertyValue(variableName).trim();
  const rgbMatch = raw.match(/rgba?\(\s*([\d.]+)\s*[,\s]\s*([\d.]+)\s*[,\s]\s*([\d.]+)/i);
  if (rgbMatch) {
    return [Number(rgbMatch[1]), Number(rgbMatch[2]), Number(rgbMatch[3])];
  }
  if (raw.startsWith("#")) {
    const hex = raw.slice(1);
    if (hex.length === 3) {
      return [
        parseInt(hex[0]! + hex[0]!, 16),
        parseInt(hex[1]! + hex[1]!, 16),
        parseInt(hex[2]! + hex[2]!, 16),
      ];
    }
    if (hex.length >= 6) {
      return [
        parseInt(hex.slice(0, 2), 16),
        parseInt(hex.slice(2, 4), 16),
        parseInt(hex.slice(4, 6), 16),
      ];
    }
  }
  return fallback;
}

/** Render a full-viewport fluid ripple canvas with displaced signal dot matrix and wake interpolation. */
function SignalRippleCanvas() {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const glowRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) {
      return;
    }
    const ctx = canvas.getContext("2d");
    if (!ctx) {
      return;
    }

    const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    let width = window.innerWidth;
    let height = window.innerHeight;
    let dpr = Math.min(window.devicePixelRatio || 1, 2);
    let accentRGB = readThemeRGB("--accent", [89, 101, 47]);
    let lineRGB = readThemeRGB("--line-strong", [201, 194, 172]);

    const pointer = {
      x: width * 0.68,
      y: height * 0.42,
      glowX: width * 0.68,
      glowY: height * 0.42,
      lastEmitX: -999,
      lastEmitY: -999,
      lastEmitAt: 0,
      active: false,
    };

    const ripples: RipplePoint[] = [];

    // Spawn a damped concentric wave while capping active rings for 60fps performance.
    const emitRipple = (
      x: number,
      y: number,
      strength: number,
      maxRadius: number,
      echo: boolean,
      duration?: number,
    ) => {
      if (reducedMotion) {
        return;
      }
      if (ripples.length >= 34) {
        ripples.shift();
      }
      ripples.push({
        x: x,
        y: y,
        age: 0,
        duration: duration ?? (echo ? 1450 : 1100),
        maxRadius: maxRadius,
        strength: strength,
        echo: echo,
      });
    };

    // Keep canvas pixel density aligned with viewport resizes.
    const resize = () => {
      width = window.innerWidth;
      height = window.innerHeight;
      dpr = Math.min(window.devicePixelRatio || 1, 2);
      canvas.width = Math.max(1, Math.floor(width * dpr));
      canvas.height = Math.max(1, Math.floor(height * dpr));
      canvas.style.width = `${width}px`;
      canvas.style.height = `${height}px`;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    };

    const syncThemeColors = () => {
      accentRGB = readThemeRGB("--accent", [89, 101, 47]);
      lineRGB = readThemeRGB("--line-strong", [201, 194, 172]);
    };

    resize();
    syncThemeColors();

    // Seed two soft opening ripples on initial mount so the surface feels immediately responsive.
    if (!reducedMotion) {
      emitRipple(width * 0.72, Math.min(height * 0.44, 340), 0.45, 220, true, 1600);
    }

    const onPointerMove = (event: PointerEvent) => {
      pointer.x = event.clientX;
      pointer.y = event.clientY;
      pointer.active = true;

      if (pointer.lastEmitX < -500) {
        pointer.lastEmitX = event.clientX;
        pointer.lastEmitY = event.clientY;
        emitRipple(event.clientX, event.clientY, 0.36, 130, false);
        pointer.lastEmitAt = performance.now();
        return;
      }

      const dx = event.clientX - pointer.lastEmitX;
      const dy = event.clientY - pointer.lastEmitY;
      const dist = Math.hypot(dx, dy);
      const now = performance.now();

      // Interpolate wake points during rapid cursor sweeps for an unbroken silky fluid trail.
      if (dist >= 20 && now - pointer.lastEmitAt >= 18) {
        const steps = Math.min(3, Math.floor(dist / 36));
        if (steps > 1) {
          for (let s = 1; s < steps; s++) {
            const t = s / steps;
            emitRipple(
              pointer.lastEmitX + dx * t,
              pointer.lastEmitY + dy * t,
              0.28,
              115,
              false,
              920,
            );
          }
        }
        const radius = Math.min(200, 110 + dist * 0.55);
        emitRipple(event.clientX, event.clientY, 0.44, radius, false);
        pointer.lastEmitX = event.clientX;
        pointer.lastEmitY = event.clientY;
        pointer.lastEmitAt = now;
      }
    };

    const onPointerDown = (event: PointerEvent) => {
      pointer.x = event.clientX;
      pointer.y = event.clientY;
      pointer.active = true;
      emitRipple(event.clientX, event.clientY, 0.85, 270, true, 1500);
    };

    const themeObserver = new MutationObserver(() => {
      syncThemeColors();
    });
    themeObserver.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-theme"],
    });

    const colorQuery = window.matchMedia("(prefers-color-scheme: dark)");
    colorQuery.addEventListener("change", syncThemeColors);
    window.addEventListener("resize", resize);
    window.addEventListener("pointermove", onPointerMove, { passive: true });
    window.addEventListener("pointerdown", onPointerDown, { passive: true });

    let rafId = 0;
    let lastTime = performance.now();
    let ambientTimer = 1800;

    // Draw the physically displaced dot matrix, fluid gradient rings, and smooth cursor glow.
    const render = (now: number) => {
      const dt = Math.min(48, Math.max(0, now - lastTime));
      lastTime = now;

      if (!document.hidden) {
        // Interpolate the soft cursor glow smoothly toward pointer coordinates.
        pointer.glowX += (pointer.x - pointer.glowX) * 0.14;
        pointer.glowY += (pointer.y - pointer.glowY) * 0.14;
        if (glowRef.current) {
          glowRef.current.style.setProperty("--cursor-x", `${pointer.glowX.toFixed(1)}px`);
          glowRef.current.style.setProperty("--cursor-y", `${pointer.glowY.toFixed(1)}px`);
        }

        // Emit a quiet ambient sonar pulse near the hero workbench when idle.
        ambientTimer += dt;
        if (ambientTimer >= 3600 && !reducedMotion) {
          ambientTimer = 0;
          emitRipple(width * 0.72, Math.min(height * 0.44, 340), 0.3, 230, true, 1650);
        }

        for (let i = ripples.length - 1; i >= 0; i--) {
          ripples[i]!.age += dt;
          if (ripples[i]!.age >= ripples[i]!.duration) {
            ripples.splice(i, 1);
          }
        }

        ctx.clearRect(0, 0, width, height);

        const spacing = 30;
        const cols = Math.ceil(width / spacing) + 1;
        const rows = Math.ceil(height / spacing) + 1;

        for (let row = 0; row < rows; row++) {
          const baseY = row * spacing;
          for (let col = 0; col < cols; col++) {
            const baseX = col * spacing;
            let energy = 0;
            let dispX = 0;
            let dispY = 0;

            if (pointer.active) {
              const dx = baseX - pointer.glowX;
              const dy = baseY - pointer.glowY;
              const pDist = Math.hypot(dx, dy);
              if (pDist < 190 && pDist > 0.5) {
                const influence = 1 - pDist / 190;
                energy += influence * 0.58;
                dispX += (dx / pDist) * influence * 2.1;
                dispY += (dy / pDist) * influence * 2.1;
              }
            }

            // Displace grid points radially along each passing wave front.
            for (let k = 0; k < ripples.length; k++) {
              const r = ripples[k]!;
              const u = r.age / r.duration;
              const eased = 1 - Math.pow(1 - u, 3);
              const waveRadius = 10 + r.maxRadius * eased;
              const dx = baseX - r.x;
              const dy = baseY - r.y;
              const dist = Math.hypot(dx, dy);
              const band = Math.abs(dist - waveRadius);
              if (band < 40 && dist > 0.5) {
                const wave = (1 - band / 40) * (1 - u) * r.strength * 1.45;
                energy = Math.min(1.25, energy + wave);
                dispX += (dx / dist) * wave * 4.4;
                dispY += (dy / dist) * wave * 4.4;
              }
            }

            const px = baseX + dispX;
            const py = baseY + dispY;
            const dotRadius = 0.85 + Math.min(1.75, energy * 1.35);

            if (energy > 0.06) {
              const mix = Math.min(1, energy);
              const rCol = Math.round(lineRGB[0] + (accentRGB[0] - lineRGB[0]) * mix);
              const gCol = Math.round(lineRGB[1] + (accentRGB[1] - lineRGB[1]) * mix);
              const bCol = Math.round(lineRGB[2] + (accentRGB[2] - lineRGB[2]) * mix);
              ctx.fillStyle = `rgba(${rCol},${gCol},${bCol},${0.24 + mix * 0.42})`;
            } else {
              ctx.fillStyle = `rgba(${lineRGB[0]},${lineRGB[1]},${lineRGB[2]},0.22)`;
            }

            ctx.beginPath();
            ctx.arc(px, py, dotRadius, 0, Math.PI * 2);
            ctx.fill();
          }
        }

        // Paint soft luminous gradient bands and crisp concentric ripple rings.
        for (let k = 0; k < ripples.length; k++) {
          const r = ripples[k]!;
          const u = r.age / r.duration;
          const eased = 1 - Math.pow(1 - u, 3);
          const radius = 10 + r.maxRadius * eased;
          const fade = Math.sin((1 - u) * Math.PI * 0.5);
          const alpha = fade * 0.36 * r.strength;

          if (radius > 16) {
            const innerR = Math.max(0, radius - 22);
            const bandGrad = ctx.createRadialGradient(r.x, r.y, innerR, r.x, r.y, radius + 6);
            bandGrad.addColorStop(0, `rgba(${accentRGB[0]},${accentRGB[1]},${accentRGB[2]},0)`);
            bandGrad.addColorStop(
              0.72,
              `rgba(${accentRGB[0]},${accentRGB[1]},${accentRGB[2]},${alpha * 0.34})`,
            );
            bandGrad.addColorStop(1, `rgba(${accentRGB[0]},${accentRGB[1]},${accentRGB[2]},0)`);
            ctx.fillStyle = bandGrad;
            ctx.beginPath();
            ctx.arc(r.x, r.y, radius + 6, 0, Math.PI * 2);
            ctx.fill();
          }

          ctx.strokeStyle = `rgba(${accentRGB[0]},${accentRGB[1]},${accentRGB[2]},${alpha})`;
          ctx.lineWidth = 1.4;
          ctx.beginPath();
          ctx.arc(r.x, r.y, radius, 0, Math.PI * 2);
          ctx.stroke();

          if (r.echo && u > 0.1) {
            const echoU = (u - 0.1) / 0.9;
            const echoRadius = 10 + r.maxRadius * 0.74 * (1 - Math.pow(1 - echoU, 3));
            const echoAlpha = Math.sin((1 - echoU) * Math.PI * 0.5) * 0.22 * r.strength;
            ctx.strokeStyle = `rgba(${accentRGB[0]},${accentRGB[1]},${accentRGB[2]},${echoAlpha})`;
            ctx.lineWidth = 1.05;
            ctx.beginPath();
            ctx.arc(r.x, r.y, echoRadius, 0, Math.PI * 2);
            ctx.stroke();
          }
        }
      }

      if (!reducedMotion) {
        rafId = window.requestAnimationFrame(render);
      }
    };

    if (reducedMotion) {
      render(performance.now());
    } else {
      rafId = window.requestAnimationFrame(render);
    }

    return () => {
      if (rafId) {
        window.cancelAnimationFrame(rafId);
      }
      themeObserver.disconnect();
      colorQuery.removeEventListener("change", syncThemeColors);
      window.removeEventListener("resize", resize);
      window.removeEventListener("pointermove", onPointerMove);
      window.removeEventListener("pointerdown", onPointerDown);
    };
  }, []);

  return (
    <>
      <div
        ref={glowRef}
        aria-hidden="true"
        className="pointer-events-none fixed inset-0 z-0 transition-opacity duration-500"
        style={{
          background:
            "radial-gradient(660px circle at var(--cursor-x, 68%) var(--cursor-y, 38%), color-mix(in srgb, var(--accent) 11%, transparent), transparent 64%)",
        }}
      />
      <canvas
        ref={canvasRef}
        aria-hidden="true"
        className="pointer-events-none fixed inset-0 z-0"
      />
    </>
  );
}

/** Render a primary link button with subtle magnetic cursor attraction and click ripple. */
function MagneticRippleLink({
  to,
  children,
  className = "",
}: {
  to: string;
  children: ReactNode;
  className?: string;
}) {
  const ref = useRef<HTMLAnchorElement | null>(null);
  const [ripple, setRipple] = useState<{ id: number; x: number; y: number } | null>(null);

  // Pull the button smoothly by up to 4px toward the cursor on hover.
  const onMouseMove = (event: ReactMouseEvent<HTMLAnchorElement>) => {
    const el = ref.current;
    if (!el || window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      return;
    }
    const rect = el.getBoundingClientRect();
    const offsetX = ((event.clientX - rect.left) / rect.width - 0.5) * 8;
    const offsetY = ((event.clientY - rect.top) / rect.height - 0.5) * 6;
    el.style.transform = `translate3d(${offsetX.toFixed(1)}px, ${(offsetY - 1).toFixed(1)}px, 0)`;
  };

  // Snap back cleanly when cursor exits.
  const onMouseLeave = () => {
    const el = ref.current;
    if (!el) {
      return;
    }
    el.style.transform = "";
  };

  // Emit a localized ripple burst inside the CTA button on click.
  const onMouseDown = (event: ReactMouseEvent<HTMLAnchorElement>) => {
    const el = ref.current;
    if (!el) {
      return;
    }
    const rect = el.getBoundingClientRect();
    setRipple({
      id: Date.now(),
      x: event.clientX - rect.left,
      y: event.clientY - rect.top,
    });
  };

  return (
    <Link
      ref={ref}
      to={to}
      onMouseMove={onMouseMove}
      onMouseLeave={onMouseLeave}
      onMouseDown={onMouseDown}
      className={`landing-shimmer-btn relative inline-flex items-center justify-center gap-2 transition-all duration-200 ease-[var(--ease-out-quart)] ${className}`}
    >
      {ripple && (
        <span
          key={ripple.id}
          aria-hidden="true"
          className="landing-click-ripple size-28"
          style={{
            left: `${ripple.x}px`,
            top: `${ripple.y}px`,
          }}
        />
      )}
      <span className="relative z-10 inline-flex items-center gap-2">{children}</span>
    </Link>
  );
}

/** Wrap a surface with cursor-tracking radial spotlight glow and subtle optional 3D tilt. */
function SpotlightCard({
  children,
  className = "",
  tilt = false,
}: {
  children: ReactNode;
  className?: string;
  tilt?: boolean;
}) {
  const cardRef = useRef<HTMLDivElement | null>(null);
  const [ripple, setRipple] = useState<{ id: number; x: number; y: number } | null>(null);

  // Update local CSS pointer coordinates and gentle 3D perspective angle.
  const handlePointerMove = (event: ReactMouseEvent<HTMLDivElement>) => {
    const el = cardRef.current;
    if (!el) {
      return;
    }
    const rect = el.getBoundingClientRect();
    const x = event.clientX - rect.left;
    const y = event.clientY - rect.top;

    el.style.setProperty("--mx", `${x.toFixed(1)}px`);
    el.style.setProperty("--my", `${y.toFixed(1)}px`);

    if (tilt && !window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      const rx = ((y / rect.height) - 0.5) * -3.6;
      const ry = ((x / rect.width) - 0.5) * 4.2;
      el.style.transform = `perspective(1100px) rotateX(${rx.toFixed(2)}deg) rotateY(${ry.toFixed(2)}deg) translateY(-2px)`;
    }
  };

  // Reset surface tilt cleanly once cursor leaves.
  const handlePointerLeave = () => {
    const el = cardRef.current;
    if (!el) {
      return;
    }
    if (tilt) {
      el.style.transform = "";
    }
  };

  // Trigger a soft local ink ripple where user clicks inside the card.
  const handlePointerDown = (event: ReactMouseEvent<HTMLDivElement>) => {
    const el = cardRef.current;
    if (!el) {
      return;
    }
    const rect = el.getBoundingClientRect();
    setRipple({
      id: Date.now(),
      x: event.clientX - rect.left,
      y: event.clientY - rect.top,
    });
  };

  return (
    <div
      ref={cardRef}
      onMouseMove={handlePointerMove}
      onMouseLeave={handlePointerLeave}
      onMouseDown={handlePointerDown}
      className={`landing-spotlight ${className}`}
    >
      {ripple && (
        <span
          key={ripple.id}
          aria-hidden="true"
          className="landing-click-ripple size-44"
          style={{
            left: `${ripple.x}px`,
            top: `${ripple.y}px`,
          }}
        />
      )}
      <div className="relative z-10">{children}</div>
    </div>
  );
}

/** Reveal sections smoothly as they enter the viewport. */
function RevealSection({
  id,
  children,
  className = "",
}: {
  id?: string;
  children: ReactNode;
  className?: string;
}) {
  const ref = useRef<HTMLElement | null>(null);
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    const node = ref.current;
    if (!node) {
      return;
    }
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches || !("IntersectionObserver" in window)) {
      setVisible(true);
      return;
    }

    // Reveal immediately if already inside or above the viewport fold on mount.
    if (node.getBoundingClientRect().top < window.innerHeight * 0.94) {
      setVisible(true);
      return;
    }

    // Reveal sections smoothly right before they enter the center viewport.
    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (entry.isIntersecting) {
            setVisible(true);
            observer.disconnect();
            break;
          }
        }
      },
      {
        threshold: 0.06,
        rootMargin: "0px 0px -12px 0px",
      },
    );

    observer.observe(node);
    return () => observer.disconnect();
  }, []);

  return (
    <section
      id={id}
      ref={ref}
      data-visible={visible ? "true" : "false"}
      className={`landing-reveal ${className}`}
    >
      {children}
    </section>
  );
}

/** Render the interactive Hero workspace preview console with 3 views and locked frame geometry. */
function PreviewConsole() {
  const [tab, setTab] = useState<"hot" | "funnel" | "daily">("hot");
  const [filter, setFilter] = useState<"all" | "model" | "opensource">("all");
  const [expandedId, setExpandedId] = useState<string>("preview-1");

  // Filter sample hot items for the active category chip.
  const visibleItems = PREVIEW_ITEMS.filter((item) => {
    if (filter === "all") {
      return true;
    }

    return item.category === filter;
  });

  // Resolve active console header title in one mapping so widths/heights stay predictable.
  const consoleTitleMap = {
    hot: "今天值得留意",
    funnel: "四级漏斗实时降噪",
    daily: "北京时间 08:00 每日简报",
  } as const;

  const isHot = tab === "hot";
  const isFunnel = tab === "funnel";
  const isDaily = tab === "daily";

  return (
    <SpotlightCard
      tilt
      className="rounded-[24px] border border-line bg-surface/95 p-5 shadow-[0_28px_85px_-46px_rgba(20,32,37,.42)] backdrop-blur-sm sm:p-6"
    >
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-line pb-4">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 text-[11px] text-ink-4">
            <span className="inline-flex size-1.5 rounded-full bg-accent" />
            个人工作区预览
          </div>
          <div className="mt-1 truncate text-[17px] font-semibold text-ink">
            {consoleTitleMap[tab]}
          </div>
        </div>

        <div
          role="tablist"
          aria-label="预览视图切换"
          className="flex shrink-0 items-center gap-1 rounded-full border border-line bg-bg-sunk p-1"
        >
          {[
            {
              key: "hot",
              label: "热点榜",
            },
            {
              key: "funnel",
              label: "漏斗",
            },
            {
              key: "daily",
              label: "08:00 日报",
            },
          ].map((item) => (
            <button
              key={item.key}
              type="button"
              role="tab"
              aria-selected={tab === item.key}
              onClick={() => setTab(item.key as "hot" | "funnel" | "daily")}
              className={`rounded-full px-2.5 py-1 text-[11px] font-medium transition-all duration-200 ${
                tab === item.key
                  ? "bg-surface text-ink shadow-[var(--shadow-card)]"
                  : "text-ink-4 hover:text-ink-2"
              }`}
            >
              {item.label}
            </button>
          ))}
        </div>
      </div>

      {/* Stack all 3 views in one CSS Grid track so switching tabs or sub-filters keeps the card height invariant. */}
      <div className="grid grid-cols-1 grid-rows-1 pt-3.5">
        <div
          aria-hidden={!isHot}
          className={`col-start-1 row-start-1 flex flex-col transition-all duration-300 ease-[var(--ease-out-quart)] ${
            isHot
              ? "translate-y-0 opacity-100"
              : "pointer-events-none invisible translate-y-1.5 opacity-0"
          }`}
        >
          <div className="mb-2 flex items-center justify-between gap-2">
            <div className="flex flex-wrap gap-1.5">
              {[
                {
                  key: "all",
                  label: "全部命中",
                },
                {
                  key: "model",
                  label: "大模型",
                },
                {
                  key: "opensource",
                  label: "开源工具",
                },
              ].map((chip) => (
                <button
                  key={chip.key}
                  type="button"
                  tabIndex={isHot ? undefined : -1}
                  onClick={() => setFilter(chip.key as "all" | "model" | "opensource")}
                  className={`rounded-full border px-2.5 py-0.5 text-[11px] transition-colors duration-150 ${
                    filter === chip.key
                      ? "border-accent/40 bg-accent-soft text-accent"
                      : "border-line-soft bg-bg-sunk/70 text-ink-4 hover:text-ink-2"
                  }`}
                >
                  {chip.label}
                </button>
              ))}
            </div>
            <span className="mono text-[10.5px] text-ink-4">48H TOP 10</span>
          </div>

          <div className="grid grid-cols-1 grid-rows-1">
            {/* Hidden baseline preserves full 3-row geometry across sub-filters on both desktop and mobile widths. */}
            <div
              aria-hidden="true"
              className="pointer-events-none invisible col-start-1 row-start-1 select-none divide-y divide-transparent"
            >
              {PREVIEW_ITEMS.map((item) => (
                <div key={`ghost-${item.id}`} className="flex gap-3 py-3.5 pl-2.5">
                  <div className="mt-0.5 size-8 shrink-0" />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-start justify-between gap-2">
                      <div className="text-[13.5px] font-semibold leading-snug">{item.title}</div>
                      <span className="mt-0.5 size-3.5 shrink-0" />
                    </div>
                    <p className="mt-1 text-[12px] leading-relaxed">{item.text}</p>
                    <div className="mt-2 flex flex-wrap items-center gap-2 text-[10.5px]">
                      <span>{item.source}</span>
                      <span>·</span>
                      <span className="mono">{item.time}</span>
                      <span className="rounded-full px-2 py-0.5 text-[10.5px]">
                        {item.matchHint}
                      </span>
                    </div>
                  </div>
                </div>
              ))}
            </div>

            <div className="col-start-1 row-start-1 divide-y divide-line">
              {visibleItems.map((item) => {
                const isOpen = expandedId === item.id;
                return (
                  <button
                    key={item.id}
                    type="button"
                    tabIndex={isHot ? undefined : -1}
                    onClick={() => setExpandedId(item.id)}
                    onMouseEnter={() => setExpandedId(item.id)}
                    className={`group relative flex w-full gap-3 py-3.5 text-left transition-all duration-200 ${
                      isOpen ? "pl-2.5" : "hover:pl-1.5"
                    }`}
                  >
                    <span
                      aria-hidden="true"
                      className={`absolute left-0 top-3.5 bottom-3.5 w-[2.5px] rounded-full bg-accent transition-opacity duration-200 ${
                        isOpen ? "opacity-100" : "opacity-0 group-hover:opacity-45"
                      }`}
                    />
                    <div
                      className={`mono mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-control text-[11px] font-semibold transition-transform duration-200 ${
                        isOpen
                          ? "scale-105 bg-accent text-accent-contrast shadow-sm"
                          : "bg-accent-soft text-accent"
                      }`}
                    >
                      {item.score}
                    </div>
                    <div className="min-w-0 flex-1">
                      <div className="flex items-start justify-between gap-2">
                        <div className="text-[13.5px] font-semibold leading-snug text-ink">
                          {item.title}
                        </div>
                        <IconArrowUpRight
                          className={`mt-0.5 shrink-0 transition-transform duration-200 ${
                            isOpen
                              ? "translate-x-0.5 -translate-y-0.5 text-accent"
                              : "text-ink-4 opacity-50 group-hover:opacity-100"
                          }`}
                          size={14}
                        />
                      </div>
                      <p className="mt-1 text-[12px] leading-relaxed text-ink-3">{item.text}</p>
                      <div className="mt-2 flex flex-wrap items-center gap-2 text-[10.5px] text-ink-4">
                        <span>{item.source}</span>
                        <span>·</span>
                        <span className="mono">{item.time}</span>
                        <span
                          className={`rounded-full bg-accent-softer px-2 py-0.5 text-[10.5px] text-accent transition-opacity duration-200 ${
                            isOpen ? "opacity-100" : "opacity-0 group-hover:opacity-70"
                          }`}
                        >
                          {item.matchHint}
                        </span>
                      </div>
                    </div>
                  </button>
                );
              })}

              {filter !== "all" && (
                <div className="pt-4">
                  <div className="flex items-center justify-between gap-2 rounded-tile border border-dashed border-line bg-bg-sunk/45 px-3.5 py-2.5 text-[11.5px] text-ink-4">
                    <span>当前仅展示分类筛选后的命中样本</span>
                    <button
                      type="button"
                      tabIndex={isHot ? undefined : -1}
                      onClick={() => setFilter("all")}
                      className="shrink-0 font-medium text-accent transition-colors hover:text-accent/80"
                    >
                      显示全部命中
                    </button>
                  </div>
                </div>
              )}
            </div>
          </div>
        </div>

        <div
          aria-hidden={!isFunnel}
          className={`col-start-1 row-start-1 flex flex-col justify-between gap-3 pt-0.5 transition-all duration-300 ease-[var(--ease-out-quart)] ${
            isFunnel
              ? "translate-y-0 opacity-100"
              : "pointer-events-none invisible translate-y-1.5 opacity-0"
          }`}
        >
          <div className="space-y-3">
            {[
              {
                label: "01 公共/隔离信源采集（已切掉过往回填）",
                count: "38 篇",
                pct: 100,
                tone: "bg-line-strong",
              },
              {
                label: "02 本地关键词 & 实体命中（0 Token）",
                count: "18 篇",
                pct: 47,
                tone: "bg-ink-4",
              },
              {
                label: "03 Batch Triage 1 次批量初筛",
                count: "Top 5",
                pct: 16,
                tone: "bg-accent/70",
              },
              {
                label: "04 个人模型逐篇精读评分入榜",
                count: "5 条",
                pct: 16,
                tone: "bg-accent",
              },
            ].map((row) => (
              <div key={row.label} className="rounded-tile border border-line-soft bg-bg-sunk/55 p-3.5">
                <div className="flex items-center justify-between gap-2 text-[12px]">
                  <span className="font-medium text-ink-2">{row.label}</span>
                  <span className="mono font-semibold text-accent">{row.count}</span>
                </div>
                <div className="mt-2 h-1.5 w-full overflow-hidden rounded-full bg-bg-muted">
                  <div
                    className={`h-full rounded-full transition-all duration-500 ${row.tone}`}
                    style={{ width: `${row.pct}%` }}
                  />
                </div>
              </div>
            ))}
          </div>

          <div className="flex items-center justify-between rounded-tile bg-accent-softer px-3.5 py-3 text-[11.5px] text-accent">
            <span className="flex items-center gap-1.5 font-medium">
              <IconSparkles size={14} />
              仅消耗 6 次模型请求完成 38 篇筛查
            </span>
            <span className="mono font-semibold">节省 ~84%</span>
          </div>
        </div>

        <div
          aria-hidden={!isDaily}
          className={`col-start-1 row-start-1 flex flex-col justify-between gap-3 pt-0.5 transition-all duration-300 ease-[var(--ease-out-quart)] ${
            isDaily
              ? "translate-y-0 opacity-100"
              : "pointer-events-none invisible translate-y-1.5 opacity-0"
          }`}
        >
          <div className="rounded-tile border border-line-soft bg-bg-sunk/60 p-4">
            <div className="flex items-center justify-between text-[11px] text-ink-4">
              <span className="flex items-center gap-1.5">
                <IconClock size={13} className="text-accent" />
                每日 08:00 准时成刊
              </span>
              <span className="mono">此前 24 小时汇总</span>
            </div>
            <h3 className="editorial-title mt-2.5 text-[16px] font-semibold text-ink">
              早安，昨晚共有 5 条高相关技术与开源更新
            </h3>
            <p className="mt-2 text-[12.5px] leading-relaxed text-ink-3">
              系统自动汇总过去 24 小时你已完成加工的个人结果，调用你的专属模型提炼当日脉络，绝不混入公共池未命中条目。
            </p>
            <div className="mt-3 flex flex-wrap gap-1.5">
              {["个人结果独享", "无额外并发回填", "历史 30 天归档"].map((tag) => (
                <span key={tag} className="chip text-[10.5px]">
                  <IconCheck size={11} className="text-accent" />
                  {tag}
                </span>
              ))}
            </div>
          </div>

          <div className="rounded-tile border border-line-soft bg-bg-sunk/40 p-3.5">
            <div className="mb-2 flex items-center justify-between text-[11px] text-ink-4">
              <span className="font-medium text-ink-2">本日简报重点摘录</span>
              <span className="mono">共 2 条主线</span>
            </div>
            <div className="space-y-2 text-[12px]">
              <div className="flex items-start justify-between gap-3 rounded-control bg-surface/80 px-3 py-2">
                <span className="leading-snug text-ink-2">
                  主线 01 · 推理与长上下文能力成为本期核心焦点，建议优先关注模型路由评估。
                </span>
                <span className="mono shrink-0 text-[11px] font-semibold text-accent">94</span>
              </div>
              <div className="flex items-start justify-between gap-3 rounded-control bg-surface/80 px-3 py-2">
                <span className="leading-snug text-ink-2">
                  主线 02 · 本地量化部署并发调度补丁合并，适合作为低成本推理备选方案。
                </span>
                <span className="mono shrink-0 text-[11px] font-semibold text-accent">88</span>
              </div>
            </div>
          </div>
        </div>
      </div>
    </SpotlightCard>
  );
}

/** Render the official silky homepage with interactive canvas ripples and modular narrative sections. */
export function RippleLanding({ registrationEnabled }: { registrationEnabled: boolean }) {
  const [scrolled, setScrolled] = useState(false);
  const [scrollProgress, setScrollProgress] = useState(0);
  const [activeFunnel, setActiveFunnel] = useState<number>(2);

  useEffect(() => {
    // Track scroll elevation and smooth top hairline progress.
    const onScroll = () => {
      const y = window.scrollY;
      const maxScroll = Math.max(1, document.documentElement.scrollHeight - window.innerHeight);
      setScrolled(y > 14);
      setScrollProgress(Math.min(1, Math.max(0, y / maxScroll)));
    };

    onScroll();
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, []);

  const currentFunnel = FUNNEL_STAGES[activeFunnel] ?? FUNNEL_STAGES[0]!;

  return (
    <div className="relative min-h-dvh overflow-x-clip bg-bg text-ink">
      <SignalRippleCanvas />

      <header
        className={`sticky top-0 z-40 transition-all duration-300 ${
          scrolled
            ? "border-b border-line/80 bg-bg/82 shadow-[0_8px_32px_-20px_rgba(20,32,37,.22)] backdrop-blur-md"
            : "border-b border-transparent bg-transparent"
        }`}
      >
        <div className="mx-auto flex h-16 max-w-[1120px] items-center justify-between gap-4 px-5">
          <Link
            to="/"
            className="group flex items-center gap-2.5 text-[17px] font-semibold tracking-tight text-ink"
          >
            <span className="flex size-9 items-center justify-center rounded-full bg-accent text-accent-contrast shadow-sm transition-transform duration-300 ease-[var(--ease-out-quart)] group-hover:scale-105 group-hover:rotate-6">
              <IconBolt size={18} />
            </span>
            <span>{SITE.name}</span>
            <span className="mono hidden rounded-full border border-line bg-surface/80 px-2 py-0.5 text-[10px] font-medium uppercase tracking-[.12em] text-ink-4 sm:inline-flex">
              Signal Desk
            </span>
          </Link>

          <nav className="hidden items-center gap-6 text-[13px] font-medium text-ink-3 md:flex">
            <a href="#pipeline" className="transition-colors hover:text-ink">
              四级降噪
            </a>
            <a href="#isolation" className="transition-colors hover:text-ink">
              BYOK 与隔离
            </a>
            <Link to="/about" className="transition-colors hover:text-ink">
              了解 {SITE.name}
            </Link>
          </nav>

          <div className="flex items-center gap-3">
            <ThemeSwitch className="hidden w-[108px] sm:grid" />
            <Link
              to="/login"
              className="inline-flex h-8.5 items-center rounded-full border border-line-strong bg-surface/85 px-4 text-[13px] font-medium text-ink-2 transition-all duration-200 hover:border-ink-4 hover:bg-surface"
            >
              登录
            </Link>
            {registrationEnabled && (
              <Link
                to="/register"
                className="landing-shimmer-btn hidden h-8.5 items-center gap-1 rounded-full bg-accent px-4 text-[12.5px] font-semibold text-accent-contrast transition-colors hover:bg-accent-ink sm:inline-flex"
              >
                注册
                <IconArrowUpRight size={14} />
              </Link>
            )}
          </div>
        </div>
        <div
          aria-hidden="true"
          className="h-[1.5px] w-full origin-left bg-accent/70 transition-transform duration-75"
          style={{ transform: `scaleX(${scrollProgress.toFixed(4)})` }}
        />
      </header>

      <main className="relative z-10">
        {/* Hero stage */}
        <section className="mx-auto grid max-w-[1120px] gap-12 px-5 pb-20 pt-12 md:grid-cols-[1.08fr_.92fr] md:items-center md:pb-28 md:pt-20">
          <div className="animate-fade-up">
            <div className="inline-flex max-w-full items-center gap-2 rounded-full border border-line bg-surface/85 px-3 py-1.5 shadow-[var(--shadow-card)] backdrop-blur-xs sm:gap-2.5 sm:px-3.5">
              <span className="landing-live-dot relative inline-flex size-2 shrink-0 rounded-full bg-accent" />
              <span className="mono truncate text-[10px] uppercase tracking-[.12em] text-accent sm:text-[11px] sm:tracking-[.16em]">
                Signal Desk · 每分钟增量扫描
              </span>
            </div>

            <h1 className="editorial-title mt-6 max-w-[640px] text-[36px] font-semibold leading-[1.12] tracking-[-.04em] text-ink sm:text-[48px] lg:text-[50px]">
              把你关注的
              <span className="whitespace-nowrap">变化，</span>
              <br />
              <span className="whitespace-nowrap">整理成</span>
              <span className="relative inline-block whitespace-nowrap text-accent">
                每天看得完
                <svg
                  aria-hidden="true"
                  viewBox="0 0 240 12"
                  fill="none"
                  className="pointer-events-none absolute -bottom-1 left-0 h-2.5 w-full opacity-55"
                >
                  <path
                    d="M2 9C62 2.5 178 2.5 238 8.5"
                    stroke="currentColor"
                    strokeWidth="2.5"
                    strokeLinecap="round"
                  />
                </svg>
              </span>
              <span className="whitespace-nowrap">的热点。</span>
            </h1>

            <p className="mt-6 max-w-[540px] text-[15.5px] leading-8 text-ink-3 sm:text-[16px]">
              选择公司和关键词，添加 RSS / Atom 订阅并接入你自己的 OpenAI 兼容模型。
              {SITE.name} 通过本地初筛与批量精选剔除噪声，为你生成私人摘要、48 小时热点榜和每日 08:00 简报。
            </p>

            <div className="mt-8 flex flex-wrap items-center gap-3">
              <MagneticRippleLink
                to={registrationEnabled ? "/register" : "/login"}
                className="h-11 rounded-full bg-accent px-6 text-[14px] font-semibold text-accent-contrast shadow-[0_12px_28px_-14px_var(--accent)] hover:bg-accent-ink"
              >
                {registrationEnabled ? "注册并开始使用" : "登录工作区"}
                <IconArrowUpRight size={16} />
              </MagneticRippleLink>
              <Link
                to="/about"
                className="inline-flex h-11 items-center rounded-full border border-line-strong bg-surface/80 px-5 text-[13px] font-medium text-ink-2 backdrop-blur-xs transition-all duration-200 hover:-translate-y-0.5 hover:bg-surface"
              >
                了解 {SITE.name}
              </Link>
              <a
                href="#pipeline"
                className="hidden h-11 items-center px-2 text-[13px] font-medium text-ink-4 transition-colors hover:text-accent sm:inline-flex"
              >
                查看运作原理 ↓
              </a>
            </div>

            {!registrationEnabled && (
              <p className="mt-3 text-[12px] text-ink-4">
                首版注册暂时关闭，使用规则与隐私说明确认后开放；已有账号可直接登录。
              </p>
            )}

            <div className="mt-8 flex flex-wrap gap-2">
              {[
                { icon: <IconRss size={13} />, label: "共享信源一次抓取" },
                { icon: <IconFilter size={13} />, label: "Batch Triage 省 ~80% 调用" },
                { icon: <IconPlug size={13} />, label: "BYOK 密钥加密隔离" },
              ].map((spec) => (
                <span
                  key={spec.label}
                  className="inline-flex items-center gap-1.5 rounded-full border border-line-soft bg-surface/75 px-3 py-1 text-[11.5px] text-ink-3 backdrop-blur-xs transition-colors hover:border-accent/35"
                >
                  <span className="text-accent">{spec.icon}</span>
                  {spec.label}
                </span>
              ))}
            </div>
          </div>

          <PreviewConsole />
        </section>

        {/* Section 1: 4-Stage Denoising Funnel */}
        <RevealSection id="pipeline" className="mx-auto max-w-[1120px] scroll-mt-20 px-5 py-16">
          <div className="flex flex-wrap items-end justify-between gap-4 border-t border-line pt-12">
            <div>
              <div className="mono text-[11px] uppercase tracking-[.16em] text-accent">
                4-STAGE SIGNAL FUNNEL
              </div>
              <h2 className="editorial-title mt-2 text-[28px] font-semibold tracking-tight text-ink sm:text-[34px]">
                不逐篇盲目烧 Token，四道漏斗留下真正值得看的变化
              </h2>
            </div>
            <p className="max-w-[360px] text-[13px] leading-relaxed text-ink-4">
              点击或悬停下方任意环节，查看从 RSS 抓取到个人热点出炉的流转细节。
            </p>
          </div>

          <div className="mt-8 grid gap-3.5 sm:grid-cols-2 lg:grid-cols-4">
            {FUNNEL_STAGES.map((stage, index) => {
              const isActive = activeFunnel === index;
              return (
                <button
                  key={stage.step}
                  type="button"
                  onClick={() => setActiveFunnel(index)}
                  onMouseEnter={() => setActiveFunnel(index)}
                  className={`landing-spotlight rounded-card border p-5 text-left transition-all duration-200 ${
                    isActive
                      ? "border-accent/55 bg-surface shadow-[0_18px_46px_-30px_rgba(20,32,37,.35)]"
                      : "border-line bg-surface/65 hover:bg-surface"
                  }`}
                >
                  <div className="relative z-10">
                    <div className="flex items-center justify-between gap-2">
                      <span
                        className={`mono text-[12px] font-semibold ${
                          isActive ? "text-accent" : "text-ink-4"
                        }`}
                      >
                        STEP {stage.step}
                      </span>
                      <span className="rounded-full bg-accent-soft px-2 py-0.5 text-[10.5px] font-medium text-accent">
                        {stage.badge}
                      </span>
                    </div>
                    <h3 className="mt-3 text-[15.5px] font-semibold text-ink">{stage.title}</h3>
                    <p className="mt-2 text-[12.5px] leading-relaxed text-ink-3">
                      {stage.summary}
                    </p>
                  </div>
                </button>
              );
            })}
          </div>

          <SpotlightCard className="mt-4 rounded-card border border-line bg-surface/90 p-5 sm:p-6">
            <div className="flex flex-wrap items-center justify-between gap-4">
              <div className="max-w-[680px]">
                <div className="mono text-[11px] uppercase tracking-[.14em] text-accent">
                  STAGE {currentFunnel.step} DETAIL · {currentFunnel.title}
                </div>
                <p className="mt-2 text-[13.5px] leading-relaxed text-ink-2">
                  {currentFunnel.detail}
                </p>
              </div>
              <div className="rounded-tile border border-line-soft bg-bg-sunk/70 px-4 py-3">
                <div className="text-[11px] text-ink-4">{currentFunnel.metricLabel}</div>
                <div className="mono mt-1 text-[13px] font-semibold text-accent">
                  {currentFunnel.metricValue}
                </div>
              </div>
            </div>
          </SpotlightCard>
        </RevealSection>

        {/* Section 2: Core Isolation & Architecture Bento */}
        <RevealSection id="isolation" className="mx-auto max-w-[1120px] scroll-mt-20 px-5 py-12">
          <div className="grid gap-5 md:grid-cols-3">
            <SpotlightCard className="rounded-[20px] border border-line bg-surface/90 p-6">
              <div className="flex items-center justify-between">
                <span className="mono text-[11px] font-semibold text-accent">01 · 自己的模型</span>
                <IconPlug size={16} className="text-ink-4" />
              </div>
              <h3 className="mt-3 text-[17px] font-semibold text-ink">BYOK 加密直连，拒绝额度焦虑</h3>
              <p className="mt-2.5 text-[13px] leading-relaxed text-ink-3">
                API Key 使用 AES-256-GCM 加密落库，所有分析与简报任务只走你自己的 OpenAI 兼容模型，并内置每日调用上限与单源洪峰保护。
              </p>
              <div className="mt-5 rounded-tile border border-line-soft bg-bg-sunk/65 p-3">
                <div className="flex items-center justify-between text-[11px] text-ink-4">
                  <span>API Key</span>
                  <span className="mono text-ok-ink">AES-256-GCM</span>
                </div>
                <div className="mono mt-1.5 text-[11.5px] text-ink-2">
                  sk-proj-••••••••••••9a4c
                </div>
              </div>
            </SpotlightCard>

            <SpotlightCard className="rounded-[20px] border border-line bg-surface/90 p-6">
              <div className="flex items-center justify-between">
                <span className="mono text-[11px] font-semibold text-accent">02 · 只看关注项</span>
                <IconFilter size={16} className="text-ink-4" />
              </div>
              <h3 className="mt-3 text-[17px] font-semibold text-ink">关键词与公司实体先过一遍</h3>
              <p className="mt-2.5 text-[13px] leading-relaxed text-ink-3">
                同一信源在平台内共享抓取不重复发请求；随后按你订阅的关键词和行业公司实体独立筛选，仅针对后续新文章生效，不回填历史噪声。
              </p>
              <div className="mt-5 flex flex-wrap gap-1.5">
                {["OpenAI", "DeepSeek", "Agent Harness", "开源量化", "推理架构"].map((tag) => (
                  <span
                    key={tag}
                    className="rounded-full border border-line bg-bg-sunk/70 px-2.5 py-1 text-[11px] text-ink-2 transition-colors hover:border-accent/40 hover:text-accent"
                  >
                    # {tag}
                  </span>
                ))}
              </div>
            </SpotlightCard>

            <SpotlightCard className="rounded-[20px] border border-line bg-surface/90 p-6">
              <div className="flex items-center justify-between">
                <span className="mono text-[11px] font-semibold text-accent">03 · 私人结果</span>
                <IconDoc size={16} className="text-ink-4" />
              </div>
              <h3 className="mt-3 text-[17px] font-semibold text-ink">个人热点与 08:00 简报严格隔离</h3>
              <p className="mt-2.5 text-[13px] leading-relaxed text-ink-3">
                你的中文摘要、48 小时热点榜 Top 10 和每日北京时间 08:00 个人简报与其他账号完全隔离，页面禁止共享缓存，离开时支持一键清空账号数据。
              </p>
              <div className="mt-5 grid grid-cols-2 gap-2 text-[11px]">
                <div className="rounded-tile border border-line-soft bg-bg-sunk/65 px-3 py-2">
                  <div className="text-ink-4">个人热点</div>
                  <div className="mono mt-0.5 font-semibold text-ink">48h · Top 10</div>
                </div>
                <div className="rounded-tile border border-line-soft bg-bg-sunk/65 px-3 py-2">
                  <div className="text-ink-4">每日简报</div>
                  <div className="mono mt-0.5 font-semibold text-ink">08:00 · 24h</div>
                </div>
              </div>
            </SpotlightCard>
          </div>
        </RevealSection>

        {/* Section 3: Closing CTA */}
        <RevealSection className="mx-auto max-w-[1120px] px-5 pb-16 pt-8">
          <SpotlightCard className="rounded-[24px] border border-line bg-surface/95 p-8 text-center sm:p-12">
            <div className="mono text-[11px] uppercase tracking-[.16em] text-accent">
              READY TO FOCUS
            </div>
            <h2 className="editorial-title mx-auto mt-3 max-w-[540px] text-[28px] font-semibold tracking-tight text-ink sm:text-[36px]">
              少刷十个信息流，留下一份每天看得完的情报简报
            </h2>
            <p className="mx-auto mt-4 max-w-[480px] text-[14px] leading-relaxed text-ink-3">
              配置只需要三步：接入 OpenAI 兼容模型、添加关注的公司与关键词、订阅你想长期跟踪的信息源。
            </p>
            <div className="mt-7 flex flex-wrap justify-center gap-3">
              <MagneticRippleLink
                to={registrationEnabled ? "/register" : "/login"}
                className="h-11 rounded-full bg-accent px-6 text-[14px] font-semibold text-accent-contrast hover:bg-accent-ink"
              >
                {registrationEnabled ? "立即注册" : "登录工作区"}
                <IconArrowUpRight size={16} />
              </MagneticRippleLink>
              {registrationEnabled && (
                <Link
                  to="/login"
                  className="inline-flex h-11 items-center rounded-full border border-line-strong bg-bg-sunk/70 px-6 text-[13.5px] font-medium text-ink-2 transition-colors hover:bg-surface"
                >
                  已有账号登录
                </Link>
              )}
            </div>
          </SpotlightCard>
        </RevealSection>
      </main>

      <footer className="relative z-10 border-t border-line/80 bg-bg/70 backdrop-blur-xs">
        <div className="mx-auto flex max-w-[1120px] flex-wrap items-center justify-between gap-4 px-5 py-7 text-[12px] text-ink-4">
          <div className="flex items-center gap-2">
            <span className="font-semibold text-ink-2">{SITE.name}</span>
            <span>·</span>
            <span>个人信号台</span>
          </div>
          <div className="flex flex-wrap items-center gap-5">
            <Link to="/about" className="transition-colors hover:text-ink">
              关于 {SITE.name}
            </Link>
            <Link to="/terms" className="transition-colors hover:text-ink">
              使用规则
            </Link>
            <Link to="/privacy" className="transition-colors hover:text-ink">
              隐私说明
            </Link>
            <ThemeSwitch className="w-[108px] sm:hidden" />
          </div>
        </div>
      </footer>
    </div>
  );
}
