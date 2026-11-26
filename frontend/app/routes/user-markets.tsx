import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useLoaderData } from "react-router";
import type { Route } from "./+types/user-markets";
import type { Screen } from "../components/shell/screens";
import { userApiGet } from "../lib/user-api.server";
import { useLocalePreference } from "../lib/local-state";
import { getDict } from "../lib/i18n";
import {
  KlineChart,
  type KlineChartHandle,
  type MarketCandle,
  type MarketNewsMarker,
} from "../features/market/KlineChart";
import { ThemeSwitch } from "../components/shell/ThemeSwitch";
import {
  IconClose,
  IconDoc,
  IconFlame,
  IconGrid,
  IconHeart,
  IconHistory,
  IconPlug,
  IconRss,
  IconSparkles,
  IconTrendUp,
} from "../components/icons";

export const handle: Screen = { home: "hot", name: "经济行情" };

interface SymbolMeta {
  symbol: string;
  name: string;
  zhName: string;
  description: string;
  category: string;
  exchange: string;
  digits: number;
}

interface QuoteSnapshot {
  symbol: string;
  price: number;
  open: number;
  prevClose: number;
  high: number;
  low: number;
  change: number;
  changePct: number;
  volume: number;
  updatedAt: number;
  mode: string;
}

interface MarketsCatalogResponse {
  enabled: boolean;
  provider: string;
  mode: string;
  timeframes: string[];
  symbols: SymbolMeta[];
  quotes: Record<string, QuoteSnapshot>;
}

interface CandlesResponse {
  symbol: string;
  timeframe: string;
  mode: string;
  quote?: QuoteSnapshot;
  candles: MarketCandle[];
  markers: MarketNewsMarker[];
}

interface WSServerTickMessage {
  type: string;
  symbol?: string;
  tf?: string;
  candle?: MarketCandle;
  quote?: QuoteSnapshot;
}

const TIMEFRAME_LABELS: Record<string, { zh: string; en: string }> = {
  "1m": { zh: "1分", en: "1m" },
  "5m": { zh: "5分", en: "5m" },
  "1h": { zh: "1时", en: "1h" },
  "1d": { zh: "日线", en: "1D" },
  "1w": { zh: "周线", en: "1W" },
  "1M": { zh: "月线", en: "1M" },
};

/** Load initial market catalog, latest quotes, historical candles, and aligned news markers. */
export async function loader({ request }: Route.LoaderArgs) {
  const url = new URL(request.url);
  const symbol = url.searchParams.get("symbol") || "XAU/USD";
  const tf = url.searchParams.get("tf") || "1m";

  const [catalog, initialCandles] = await Promise.all([
    userApiGet<MarketsCatalogResponse>(request, "/api/me/markets"),
    userApiGet<CandlesResponse>(
      request,
      `/api/me/markets/candles?symbol=${encodeURIComponent(symbol)}&tf=${encodeURIComponent(tf)}&limit=300`
    ),
  ]);

  return {
    catalog,
    initialCandles,
  };
}

export const meta: Route.MetaFunction = () => [
  { title: "经济行情 · MyHOT" },
  { name: "robots", content: "noindex, nofollow" },
];

export const headers: Route.HeadersFunction = () => ({
  "Cache-Control": "private, no-store",
  "X-Robots-Tag": "noindex, nofollow",
});

/** Format a Unix second timestamp for news marker timeline display. */
function formatMarkerTime(epochSec: number, locale: "zh" | "en"): string {
  return new Intl.DateTimeFormat(locale === "en" ? "en-US" : "zh-CN", {
    timeZone: "Asia/Shanghai",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(new Date(epochSec * 1000));
}

/** Merge a quote only if it is at least as recent as the displayed price. */
function withMarketQuote(quotes: Record<string, QuoteSnapshot>, quote: QuoteSnapshot) {
  if ((quotes[quote.symbol]?.updatedAt ?? 0) > quote.updatedAt) {
    return quotes;
  }
  return { ...quotes, [quote.symbol]: quote };
}

/** Render the authenticated real-time economic K-line page with WebSocket streaming and news overlay. */
export default function UserMarkets() {
  const { catalog, initialCandles } = useLoaderData<typeof loader>();
  const locale = useLocalePreference();
  const dict = getDict(locale);
  const isEn = locale === "en";

  const [selectedSymbol, setSelectedSymbol] = useState(initialCandles.symbol || "XAU/USD");
  const [selectedTF, setSelectedTF] = useState(initialCandles.timeframe || "1m");
  const [candles, setCandles] = useState<MarketCandle[]>(initialCandles.candles ?? []);
  const [markers, setMarkers] = useState<MarketNewsMarker[]>(initialCandles.markers ?? []);
  const [quotes, setQuotes] = useState<Record<string, QuoteSnapshot>>(catalog.quotes ?? {});
  const [loadingWindow, setLoadingWindow] = useState(false);
  const [focusedMarkerTime, setFocusedMarkerTime] = useState<number | null>(null);
  const [drawerOpen, setDrawerOpen] = useState(false);

  const chartRef = useRef<KlineChartHandle | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const snapshotRequest = useRef(0);
  const latestLiveCandle = useRef<MarketCandle | null>(null);
  const activeSubRef = useRef({ symbol: selectedSymbol, tf: selectedTF });

  activeSubRef.current = { symbol: selectedSymbol, tf: selectedTF };

  // Close mobile drawer on Escape key.
  useEffect(() => {
    if (!drawerOpen) {
      return;
    }

    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setDrawerOpen(false);
      }
    };

    window.addEventListener("keydown", onKeyDown);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
    };
  }, [drawerOpen]);

  /** Fetch historical + live snapshot window cleanly for a symbol/timeframe without tearing down WebSocket. */
  async function fetchCandleSnapshot(symbol: string, tf: string, showLoading = false) {
    const request = ++snapshotRequest.current;
    const liveAtRequest = latestLiveCandle.current;
    if (showLoading) {
      setLoadingWindow(true);
    }
    try {
      const res = await fetch(
        `/api/me/markets/candles?symbol=${encodeURIComponent(symbol)}&tf=${encodeURIComponent(tf)}&limit=300`
      );
      if (!res.ok) {
        return;
      }

      const payload = (await res.json()) as CandlesResponse;
      if (request !== snapshotRequest.current || activeSubRef.current.symbol !== symbol || activeSubRef.current.tf !== tf) {
        return;
      }

      const history = payload.candles ?? [];
      const live = latestLiveCandle.current;
      if (live && live !== liveAtRequest && live.time >= (history.at(-1)?.time ?? 0)) {
        const beforeLive = history.at(-1)?.time === live.time ? history.slice(0, -1) : history;
        setCandles([...beforeLive, live]);
      } else {
        setCandles(history);
      }
      setMarkers(payload.markers ?? []);
      if (payload.quote) {
        const latestQuote = payload.quote;
        setQuotes((prev) => withMarketQuote(prev, latestQuote));
      }
    } catch {
      // The next snapshot or reconnect retries without clearing the last good window.
    } finally {
      if (request === snapshotRequest.current) {
        setLoadingWindow(false);
      }
    }
  }

  // Keep all three cards current even when the socket is subscribed to one chart.
  useEffect(() => {
    if (!catalog.enabled) {
      return;
    }
    const timer = window.setInterval(async () => {
      try {
        const res = await fetch("/api/me/markets");
        if (res.ok) {
          const data = (await res.json()) as MarketsCatalogResponse;
          setQuotes((prev) => Object.values(data.quotes).reduce(withMarketQuote, prev));
        }
      } catch {
        // Keep the last quoted price and its timestamp during a network outage.
      }
    }, 60_000);
    return () => window.clearInterval(timer);
  }, [catalog.enabled]);

  // Maintain one persistent same-origin WebSocket with automatic exponential backoff recovery.
  useEffect(() => {
    if (!catalog.enabled) {
      return;
    }

    let disposed = false;
    let reconnectAttempt = 0;
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
    let heartbeatTimer: ReturnType<typeof setInterval> | null = null;

    /** Open the WebSocket connection using current active subscription state. */
    function connect() {
      if (disposed) {
        return;
      }

      const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
      const { symbol, tf } = activeSubRef.current;
      const endpoint = `${protocol}//${window.location.host}/api/ws/markets?symbol=${encodeURIComponent(symbol)}&tf=${encodeURIComponent(tf)}`;

      const socket = new WebSocket(endpoint);
      wsRef.current = socket;

      socket.onopen = () => {
        if (disposed) {
          socket.close();
          return;
        }

        reconnectAttempt = 0;

        // Sync after connecting to close the gap between the page response and the subscription.
        void fetchCandleSnapshot(activeSubRef.current.symbol, activeSubRef.current.tf);

        heartbeatTimer = setInterval(() => {
          if (socket.readyState === WebSocket.OPEN) {
            socket.send(JSON.stringify({ op: "ping" }));
          }
        }, 25000);
      };

      socket.onmessage = (event) => {
        try {
          const msg = JSON.parse(String(event.data)) as WSServerTickMessage;
          if (!msg.symbol || (msg.type !== "candle" && msg.type !== "snapshot")) {
            return;
          }

          if (msg.quote) {
            const incomingQuote = msg.quote;
            setQuotes((prev) => withMarketQuote(prev, incomingQuote));
          }

          if (msg.symbol === activeSubRef.current.symbol && msg.tf === activeSubRef.current.tf) {
            if (msg.type === "snapshot") {
              void fetchCandleSnapshot(msg.symbol, activeSubRef.current.tf);
            } else if (msg.candle) {
              latestLiveCandle.current = msg.candle;
              chartRef.current?.applyLiveCandle(msg.candle);
            }
          }
        } catch {
          // Ignore non-JSON or malformed keepalive frames safely.
        }
      };

      socket.onerror = () => {
        socket.close();
      };

      socket.onclose = () => {
        if (heartbeatTimer) {
          clearInterval(heartbeatTimer);
          heartbeatTimer = null;
        }
        wsRef.current = null;

        if (disposed) {
          return;
        }

        const delayMs = Math.min(10000, 1000 * Math.pow(2, reconnectAttempt));
        reconnectAttempt += 1;
        reconnectTimer = setTimeout(connect, delayMs);
      };
    }

    connect();

    return () => {
      disposed = true;
      if (reconnectTimer) {
        clearTimeout(reconnectTimer);
      }
      if (heartbeatTimer) {
        clearInterval(heartbeatTimer);
      }
      wsRef.current?.close();
      wsRef.current = null;
    };
  }, [catalog.enabled]);

  /** Switch asset symbol or K-line timeframe smoothly over the existing WebSocket stream. */
  function handleSwitchTarget(nextSymbol: string, nextTF: string) {
    if (nextSymbol === selectedSymbol && nextTF === selectedTF) {
      return;
    }

    setSelectedSymbol(nextSymbol);
    setSelectedTF(nextTF);
    setFocusedMarkerTime(null);
    setCandles([]);
    setMarkers([]);
    latestLiveCandle.current = null;
    activeSubRef.current = { symbol: nextSymbol, tf: nextTF };

    // Sync query params cleanly without full page reload.
    const nextUrl = new URL(window.location.href);
    nextUrl.searchParams.set("symbol", nextSymbol);
    nextUrl.searchParams.set("tf", nextTF);
    window.history.replaceState(null, "", nextUrl.toString());

    const socket = wsRef.current;
    if (socket && socket.readyState === WebSocket.OPEN) {
      socket.send(
        JSON.stringify({
          op: "subscribe",
          symbol: nextSymbol,
          tf: nextTF,
        })
      );
    }

    void fetchCandleSnapshot(nextSymbol, nextTF, true);
  }

  const activeMeta = useMemo(() => {
    return catalog.symbols.find((s) => s.symbol === selectedSymbol) ?? catalog.symbols[0];
  }, [catalog.symbols, selectedSymbol]);

  const activeQuote = quotes[selectedSymbol];
  const sourceName = catalog.provider === "twelvedata" ? "Twelve Data" : catalog.provider;
  const activeStatus = activeQuote?.mode === "mock"
    ? (isEn ? "Simulated data" : "模拟行情")
    : activeQuote?.mode === "live"
      ? (isEn ? "Live quote stream" : "实时推送")
      : activeQuote
        ? (isEn ? "Scheduled updates" : "定时更新")
        : (isEn ? "No data" : "暂无数据");
  const digits = activeMeta?.digits ?? 2;

  return (
    <div className="pb-16 pt-2 lg:pt-0">
      {/* Top Bar */}
      <div className="flex flex-col gap-y-3">
        <div className="lg:hidden">
          <button
            type="button"
            onClick={() => setDrawerOpen(true)}
            aria-expanded={drawerOpen}
            aria-label={isEn ? "Open navigation drawer" : "打开功能导航抽屉"}
            className="inline-flex items-center gap-1.5 rounded-full border border-line-strong bg-surface px-3 py-1.5 text-[12.5px] font-medium text-ink shadow-xs transition-all hover:border-accent hover:text-accent active:scale-95"
          >
            <IconGrid size={14} className="text-accent" />
            <span>{isEn ? "Menu" : "功能导航"}</span>
          </button>
        </div>

        <div className="flex flex-wrap items-center justify-between gap-3">
          <h1 className="editorial-title flex items-center gap-2 text-[22px] font-semibold tracking-tight text-ink sm:text-[28px] lg:text-[32px]">
            <span>{dict.nav.markets}</span>
          </h1>
        </div>
      </div>

      {/* Mobile Navigation Drawer */}
      <div
        className={`fixed inset-0 z-50 transition-opacity duration-300 lg:hidden ${
          drawerOpen ? "pointer-events-auto opacity-100" : "pointer-events-none opacity-0"
        }`}
        aria-hidden={!drawerOpen}
      >
        <div
          className="absolute inset-0 bg-black/40 backdrop-blur-[2px]"
          onClick={() => setDrawerOpen(false)}
        />
        <aside
          className={`absolute inset-y-0 left-0 flex w-[280px] max-w-[82vw] flex-col border-r border-line bg-surface shadow-2xl transition-transform duration-300 ease-out ${
            drawerOpen ? "translate-x-0" : "-translate-x-full"
          }`}
        >
          <div className="flex h-14 items-center justify-between border-b border-line px-4">
            <div className="flex items-center gap-2">
              <IconGrid size={16} className="text-accent" />
              <span className="text-[14px] font-semibold text-ink">{isEn ? "Menu" : "功能导航"}</span>
            </div>
            <button
              type="button"
              onClick={() => setDrawerOpen(false)}
              aria-label={isEn ? "Close menu" : "关闭菜单"}
              className="flex size-8 items-center justify-center rounded-full text-ink-3 transition-colors hover:bg-bg-sunk hover:text-ink"
            >
              <IconClose size={17} />
            </button>
          </div>
          <nav className="flex-1 overflow-y-auto p-3" aria-label={isEn ? "Mobile drawer navigation" : "快捷导航抽屉"}>
            <div className="px-2 pb-1.5 pt-1 text-[11px] font-semibold uppercase tracking-wider text-ink-4">
              {dict.nav.workspace}
            </div>
            <div className="flex flex-col gap-1">
              <Link to="/hot" onClick={() => setDrawerOpen(false)} className="flex h-10 items-center gap-2.5 rounded-control px-2.5 text-[13.5px] font-medium text-ink-3 hover:bg-bg-sunk hover:text-ink">
                <span className="flex w-5 justify-center text-accent"><IconFlame size={17} /></span>
                <span>{dict.nav.hot}</span>
              </Link>
              <Link to="/recommendations" onClick={() => setDrawerOpen(false)} className="flex h-10 items-center gap-2.5 rounded-control px-2.5 text-[13.5px] font-medium text-ink-3 hover:bg-bg-sunk hover:text-ink">
                <span className="flex w-5 justify-center text-accent"><IconSparkles size={17} /></span>
                <span>{dict.nav.recommendations}</span>
              </Link>
              <Link to="/daily" onClick={() => setDrawerOpen(false)} className="flex h-10 items-center gap-2.5 rounded-control px-2.5 text-[13.5px] font-medium text-ink-3 hover:bg-bg-sunk hover:text-ink">
                <span className="flex w-5 justify-center text-accent"><IconDoc size={17} /></span>
                <span>{dict.nav.daily}</span>
              </Link>
              <Link to="/sources" onClick={() => setDrawerOpen(false)} className="flex h-10 items-center gap-2.5 rounded-control px-2.5 text-[13.5px] font-medium text-ink-3 hover:bg-bg-sunk hover:text-ink">
                <span className="flex w-5 justify-center text-accent"><IconRss size={17} /></span>
                <span>{dict.nav.sources}</span>
              </Link>
              <Link to="/interests" onClick={() => setDrawerOpen(false)} className="flex h-10 items-center gap-2.5 rounded-control px-2.5 text-[13.5px] font-medium text-ink-3 hover:bg-bg-sunk hover:text-ink">
                <span className="flex w-5 justify-center text-accent"><IconGrid size={17} /></span>
                <span>{dict.nav.interests}</span>
              </Link>
              <Link to="/model" onClick={() => setDrawerOpen(false)} className="flex h-10 items-center gap-2.5 rounded-control px-2.5 text-[13.5px] font-medium text-ink-3 hover:bg-bg-sunk hover:text-ink">
                <span className="flex w-5 justify-center text-accent"><IconPlug size={17} /></span>
                <span>{dict.nav.model}</span>
              </Link>
              <Link to="/markets" onClick={() => setDrawerOpen(false)} className="flex h-10 items-center gap-2.5 rounded-control bg-bg-sunk px-2.5 text-[13.5px] font-semibold text-accent">
                <span className="flex w-5 justify-center text-accent"><IconTrendUp size={17} /></span>
                <span>{dict.nav.markets}</span>
              </Link>
            </div>
            <div className="mt-4 border-t border-line/60 px-2 pb-1.5 pt-3 text-[11px] font-semibold uppercase tracking-wider text-ink-4">
              {dict.nav.historyAndAbout}
            </div>
            <div className="flex flex-col gap-1">
              <Link to="/history" onClick={() => setDrawerOpen(false)} className="flex h-10 items-center gap-2.5 rounded-control px-2.5 text-[13.5px] font-medium text-ink-3 hover:bg-bg-sunk hover:text-ink">
                <span className="flex w-5 justify-center text-accent"><IconHistory size={17} /></span>
                <span>{dict.nav.history}</span>
              </Link>
              <Link to="/about" onClick={() => setDrawerOpen(false)} className="flex h-10 items-center gap-2.5 rounded-control px-2.5 text-[13.5px] font-medium text-ink-3 hover:bg-bg-sunk hover:text-ink">
                <span className="flex w-5 justify-center text-accent"><IconHeart size={17} /></span>
                <span>{dict.nav.about}</span>
              </Link>
            </div>
          </nav>
          <div className="border-t border-line bg-surface/60 px-4 py-3.5">
            <div className="flex items-center justify-between gap-3">
              <span className="text-[13px] font-medium text-ink-3">{isEn ? "Theme" : "外观主题"}</span>
              <ThemeSwitch className="w-[112px]" />
            </div>
          </div>
        </aside>
      </div>

      {/* Asset Quote Cards */}
      <div className="mt-5 grid grid-cols-1 gap-3 sm:grid-cols-3">
        {catalog.symbols.map((meta) => {
          const q = quotes[meta.symbol];
          const isSelected = meta.symbol === selectedSymbol;
          const isPositive = (q?.change ?? 0) >= 0;

          return (
            <button
              key={meta.symbol}
              type="button"
              onClick={() => handleSwitchTarget(meta.symbol, selectedTF)}
              className={`group flex flex-col justify-between rounded-card border p-4 text-left transition-all ${
                isSelected
                  ? "border-accent bg-raised shadow-[var(--shadow-card-hover)] ring-1 ring-accent/25"
                  : "border-line bg-surface hover:border-line-strong hover:bg-surface-2"
              }`}
            >
              <div className="flex items-center justify-between gap-2">
                <div>
                  <div className="flex items-center gap-2">
                    <span className="text-[14px] font-semibold text-ink">
                      {isEn ? meta.name : meta.zhName}
                    </span>
                    <span className="mono rounded-mark bg-bg-sunk px-1.5 py-0.5 text-[10px] text-ink-3">
                      {meta.symbol}
                    </span>
                  </div>
                  <p className="mt-0.5 text-[11px] text-ink-4">{meta.exchange}</p>
                </div>
                <span className="rounded-full bg-bg-sunk px-2 py-0.5 text-[10.5px] text-ink-3">
                  {meta.category}
                </span>
              </div>

              <div className="mt-3 flex items-baseline justify-between gap-2">
                <span className="mono text-[20px] font-semibold tracking-tight text-ink">
                  {q ? q.price.toFixed(meta.digits) : "—"}
                </span>
                {q && q.prevClose > 0 ? (
                  <span
                    className={`mono rounded-mark px-1.5 py-0.5 text-[11.5px] font-medium ${
                      isPositive ? "bg-ok-soft text-ok-ink" : "bg-hot-soft text-hot"
                    }`}
                  >
                    {isPositive ? "+" : ""}
                    {q.change.toFixed(meta.digits)} ({isPositive ? "+" : ""}
                    {q.changePct.toFixed(2)}%)
                  </span>
                ) : !q && (
                  <span className="text-[11px] text-ink-4">{isEn ? "No quote" : "暂无报价"}</span>
                )}
              </div>
            </button>
          );
        })}
      </div>

      {/* Main K-Line Chart Card */}
      <section className="mt-4 rounded-card border border-line bg-surface p-4 shadow-[var(--shadow-card)] sm:p-5">
        <div className="mb-3 flex flex-wrap items-center justify-between gap-3 border-b border-line-soft pb-3">
          <div className="flex flex-wrap items-baseline gap-2.5">
            <h2 className="text-[16px] font-semibold text-ink">
              {isEn ? activeMeta?.name : activeMeta?.zhName} · {selectedSymbol}
            </h2>
            {activeQuote && (
              <span className="mono text-[13px] font-medium text-ink-3">
                {activeQuote.price.toFixed(digits)}
              </span>
            )}
            <span className="text-[11.5px] text-ink-4">{activeMeta?.description}</span>
            <span className="text-[11.5px] text-ink-4">
              {sourceName} · {activeStatus}
              {activeQuote && ` · ${formatMarkerTime(activeQuote.updatedAt, locale)} (UTC+8)`}
            </span>
          </div>

          <div className="flex items-center gap-2">
            {loadingWindow && (
              <span className="text-[11.5px] text-ink-4 animate-pulse">
                {isEn ? "Loading…" : "切换加载中…"}
              </span>
            )}
            <div
              role="tablist"
              aria-label={isEn ? "Timeframe selector" : "K线周期切换"}
              className="flex items-center gap-1 rounded-full border border-line bg-bg-sunk p-1 text-[11.5px]"
            >
              {catalog.timeframes.map((tf) => {
                const label = TIMEFRAME_LABELS[tf] ?? { zh: tf, en: tf };
                const active = tf === selectedTF;
                return (
                  <button
                    key={tf}
                    type="button"
                    role="tab"
                    aria-selected={active}
                    onClick={() => handleSwitchTarget(selectedSymbol, tf)}
                    className={`rounded-full px-3 py-1 transition-all ${
                      active
                        ? "bg-surface font-semibold text-accent shadow-xs"
                        : "text-ink-3 hover:text-ink"
                    }`}
                  >
                    {isEn ? label.en : label.zh}
                  </button>
                );
              })}
            </div>
          </div>
        </div>

        <KlineChart
          ref={chartRef}
          symbol={selectedSymbol}
          timeframe={selectedTF}
          digits={digits}
          candles={candles}
          markers={markers}
          onSelectMarkerTime={(t) => setFocusedMarkerTime(t)}
        />

        <p className="mt-2.5 text-[11px] text-ink-4">
          {isEn
            ? "Market data is for informational reference only and does not constitute investment advice. Markers show relevant MyHOT news."
            : "提示：页面行情与指数代理数据仅供参考，不构成任何投资建议；时间轴黄色圆点代表对应时段关联的高分财经热点事件。"}
        </p>
      </section>

      {/* Aligned News Markers Feed */}
      <section className="mt-6">
        <div className="mb-3 flex items-center justify-between">
          <h2 className="text-[15px] font-semibold text-ink">
            {isEn ? "Correlated High-Score News Events" : "K 线关联高分财经与市场热点"}
          </h2>
          {focusedMarkerTime !== null && (
            <button
              type="button"
              onClick={() => setFocusedMarkerTime(null)}
              className="text-[12px] text-accent hover:underline"
            >
              {isEn ? "Show all markers" : "显示全部关联事件"}
            </button>
          )}
        </div>

        {markers.length === 0 ? (
          <div className="rounded-card border border-line bg-surface p-6 text-center text-[13px] text-ink-4">
            {isEn
              ? "No correlated high-score articles fell within the current candle time window."
              : "当前 K 线时间窗口内暂无命中该标的的高分财经新闻。"}
          </div>
        ) : (
          <div className="grid grid-cols-1 gap-2.5 md:grid-cols-2">
            {markers
              .slice()
              .reverse()
              .map((marker) => {
                const highlighted = focusedMarkerTime !== null && marker.time === focusedMarkerTime;
                return (
                  <div
                    key={`${marker.id}-${marker.time}`}
                    className={`flex flex-col justify-between gap-2 rounded-card border p-3.5 transition-all ${
                      highlighted
                        ? "border-amber bg-amber-soft/50"
                        : "border-line bg-surface hover:border-line-strong"
                    }`}
                  >
                    <div className="flex items-start justify-between gap-2">
                      <Link
                        to={marker.href}
                        className="text-[13.5px] font-medium leading-snug text-ink hover:text-accent"
                      >
                        {marker.title}
                      </Link>
                      <span className="shrink-0 rounded-full bg-accent-soft px-2 py-0.5 text-[11px] font-semibold text-accent">
                        {Math.round(marker.score)}
                      </span>
                    </div>

                    <div className="flex items-center justify-between gap-2 text-[11.5px] text-ink-4">
                      <span>
                        {marker.source} · {formatMarkerTime(marker.time, locale)}
                      </span>
                      <button
                        type="button"
                        onClick={() => {
                          setFocusedMarkerTime(marker.time);
                          chartRef.current?.scrollToTimestamp(marker.time);
                        }}
                        className="text-accent hover:underline"
                      >
                        {isEn ? "Locate on chart" : "定位 K 线"}
                      </button>
                    </div>
                  </div>
                );
              })}
          </div>
        )}
      </section>
    </div>
  );
}
