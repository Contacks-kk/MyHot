import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from "react";
import type {
  CandlestickData,
  HistogramData,
  IChartApi,
  ISeriesApi,
  ISeriesMarkersPluginApi,
  SeriesMarker,
  Time,
  TickMarkType,
  UTCTimestamp,
} from "lightweight-charts";

export interface MarketCandle {
  time: number;
  open: number;
  high: number;
  low: number;
  close: number;
  volume: number;
}

export interface MarketNewsMarker {
  time: number;
  id: string;
  title: string;
  score: number;
  source: string;
  scope: string;
  href: string;
}

export interface KlineChartHandle {
  /** Apply a real-time incremental bar update directly onto the canvas without rebuilding chart history. */
  applyLiveCandle: (bar: MarketCandle) => void;
  /** Center the chart viewport around a specific UTC timestamp. */
  scrollToTimestamp: (epochSec: number) => void;
}

interface KlineChartProps {
  symbol: string;
  timeframe: string;
  digits: number;
  candles: MarketCandle[];
  markers: MarketNewsMarker[];
  onSelectMarkerTime?: (epochSec: number | null) => void;
}

interface OhlcvHover {
  time: number;
  open: number;
  high: number;
  low: number;
  close: number;
  volume: number;
}

/** Read live CSS design tokens from document root so Canvas matches light and dark themes. */
function readThemeTokens() {
  if (typeof window === "undefined") {
    return {
      bg: "#ffffff",
      ink: "#1d2621",
      inkMuted: "#718077",
      line: "#dcd7ca",
      up: "#15803d",
      down: "#c2410c",
      amber: "#b45309",
      volUp: "rgba(21, 128, 61, 0.22)",
      volDown: "rgba(194, 65, 12, 0.22)",
    };
  }

  const styles = getComputedStyle(document.documentElement);
  const pick = (name: string, fallback: string) => styles.getPropertyValue(name).trim() || fallback;
  const isDark = document.documentElement.getAttribute("data-theme") === "dark";

  return {
    bg: pick("--surface", isDark ? "#18221e" : "#ffffff"),
    ink: pick("--ink", isDark ? "#e9efeb" : "#1d2621"),
    inkMuted: pick("--ink-4", isDark ? "#788a80" : "#718077"),
    line: pick("--line-soft", isDark ? "#1f2c26" : "#eae6dc"),
    up: pick("--ok", isDark ? "#4ade80" : "#15803d"),
    down: pick("--hot", isDark ? "#fb7185" : "#c2410c"),
    amber: pick("--amber", isDark ? "#fbbf24" : "#b45309"),
    volUp: isDark ? "rgba(74, 222, 128, 0.24)" : "rgba(21, 128, 61, 0.22)",
    volDown: isDark ? "rgba(251, 113, 133, 0.24)" : "rgba(194, 65, 12, 0.22)",
  };
}

/** Format a UTC Unix second timestamp cleanly in Asia/Shanghai timezone. */
function formatAxisTime(epochSec: number, tf: string): string {
  const date = new Date(epochSec * 1000);
  if (tf === "1M") {
    return new Intl.DateTimeFormat("zh-CN", {
      timeZone: "Asia/Shanghai",
      year: "numeric",
      month: "2-digit",
    }).format(date);
  }

  if (tf === "1d" || tf === "1w") {
    return new Intl.DateTimeFormat("zh-CN", {
      timeZone: "Asia/Shanghai",
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
    }).format(date);
  }

  return new Intl.DateTimeFormat("zh-CN", {
    timeZone: "Asia/Shanghai",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(date);
}

/** Client-only TradingView lightweight-charts wrapper supporting live tick updates and news markers. */
export const KlineChart = forwardRef<KlineChartHandle, KlineChartProps>(function KlineChart(
  { symbol, timeframe, digits, candles, markers, onSelectMarkerTime },
  ref
) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const candleSeriesRef = useRef<ISeriesApi<"Candlestick", Time> | null>(null);
  const volumeSeriesRef = useRef<ISeriesApi<"Histogram", Time> | null>(null);
  const markerPluginRef = useRef<ISeriesMarkersPluginApi<Time> | null>(null);
  const candlesRef = useRef<MarketCandle[]>(candles);
  const lastBarTime = useRef(0);
  const fittedMarket = useRef("");
  const timeframeRef = useRef<string>(timeframe);
  const tokensRef = useRef(readThemeTokens());

  const [ready, setReady] = useState(false);
  const [hoverBar, setHoverBar] = useState<OhlcvHover | null>(null);

  candlesRef.current = candles;
  timeframeRef.current = timeframe;

  useImperativeHandle(
    ref,
    () => ({
      applyLiveCandle(bar: MarketCandle) {
        const candleSeries = candleSeriesRef.current;
        const volumeSeries = volumeSeriesRef.current;
        if (!candleSeries || !volumeSeries || bar.time < lastBarTime.current) {
          return;
        }

        lastBarTime.current = bar.time;
        const t = bar.time as UTCTimestamp;
        const tokens = tokensRef.current;
        const isUp = bar.close >= bar.open;

        candleSeries.update({
          time: t,
          open: bar.open,
          high: bar.high,
          low: bar.low,
          close: bar.close,
        });

        volumeSeries.update({
          time: t,
          value: bar.volume,
          color: isUp ? tokens.volUp : tokens.volDown,
        });

        setHoverBar((prev) => {
          if (!prev || prev.time === bar.time) {
            return { ...bar };
          }
          return prev;
        });
      },

      scrollToTimestamp(epochSec: number) {
        const chart = chartRef.current;
        if (!chart) {
          return;
        }

        const tf = timeframeRef.current;
        const halfWindow =
          tf === "1M"
            ? 86400 * 180
            : tf === "1w"
            ? 86400 * 45
            : tf === "1d"
            ? 86400 * 14
            : tf === "1h"
            ? 3600 * 36
            : 3600 * 2;
        chart.timeScale().setVisibleRange({
          from: (epochSec - halfWindow) as UTCTimestamp,
          to: (epochSec + halfWindow) as UTCTimestamp,
        });
      },
    }),
    []
  );

  // Dynamically load lightweight-charts only inside browser mount to guarantee SSR compatibility.
  useEffect(() => {
    const el = containerRef.current;
    if (!el) {
      return;
    }

    let disposed = false;
    let resizeObserver: ResizeObserver | null = null;
    let themeObserver: MutationObserver | null = null;

    /** Initialize chart canvas, series, observers, and crosshair listeners. */
    async function mountChart() {
      const lc = await import("lightweight-charts");
      if (disposed || !el) {
        return;
      }

      const tokens = readThemeTokens();
      tokensRef.current = tokens;

      const chart = lc.createChart(el, {
        width: el.clientWidth,
        height: 440,
        layout: {
          background: { color: tokens.bg },
          textColor: tokens.inkMuted,
          fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
          fontSize: 11,
        },
        grid: {
          vertLines: { color: tokens.line },
          horzLines: { color: tokens.line },
        },
        rightPriceScale: {
          borderColor: tokens.line,
          scaleMargins: {
            top: 0.1,
            bottom: 0.24,
          },
        },
        timeScale: {
          borderColor: tokens.line,
          timeVisible: true,
          secondsVisible: false,
          rightOffset: 6,
          barSpacing: 8,
          tickMarkFormatter: (time: Time, mark: TickMarkType) => {
            if (typeof time !== "number") {
              return null;
            }
            const format: Intl.DateTimeFormatOptions = mark === lc.TickMarkType.Year
              ? { year: "numeric" }
              : mark === lc.TickMarkType.Month
                ? { month: "numeric", day: "numeric" }
                : mark === lc.TickMarkType.DayOfMonth
                  ? { day: "numeric" }
                  : { hour: "2-digit", minute: "2-digit", hourCycle: "h23" };
            return new Intl.DateTimeFormat("zh-CN", {
              ...format,
              timeZone: "Asia/Shanghai",
            }).format(time * 1000);
          },
        },
        crosshair: {
          mode: 0,
        },
        localization: {
          timeFormatter: (time: Time) => {
            if (typeof time === "number") {
              return formatAxisTime(time, timeframeRef.current);
            }
            return String(time);
          },
        },
      });

      const candleSeries = chart.addSeries(lc.CandlestickSeries, {
        upColor: tokens.up,
        downColor: tokens.down,
        borderUpColor: tokens.up,
        borderDownColor: tokens.down,
        wickUpColor: tokens.up,
        wickDownColor: tokens.down,
      });

      const volumeSeries = chart.addSeries(lc.HistogramSeries, {
        priceFormat: { type: "volume" },
        priceScaleId: "",
      });

      volumeSeries.priceScale().applyOptions({
        scaleMargins: {
          top: 0.8,
          bottom: 0,
        },
      });

      const markerPlugin = lc.createSeriesMarkers(candleSeries, []);

      chart.subscribeCrosshairMove((param) => {
        if (!param.time || typeof param.time !== "number") {
          const latest = candlesRef.current.at(-1);
          setHoverBar(latest ? { ...latest } : null);
          return;
        }

        const barData = param.seriesData.get(candleSeries) as CandlestickData<Time> | undefined;
        const volData = param.seriesData.get(volumeSeries) as HistogramData<Time> | undefined;
        if (barData) {
          setHoverBar({
            time: param.time,
            open: barData.open,
            high: barData.high,
            low: barData.low,
            close: barData.close,
            volume: volData?.value ?? 0,
          });
        }
      });

      chart.subscribeClick((param) => {
        if (!onSelectMarkerTime) {
          return;
        }

        if (typeof param.time === "number") {
          onSelectMarkerTime(param.time);
        } else {
          onSelectMarkerTime(null);
        }
      });

      resizeObserver = new ResizeObserver((entries) => {
        const entry = entries[0];
        if (entry && chartRef.current) {
          chartRef.current.applyOptions({ width: Math.floor(entry.contentRect.width) });
        }
      });
      resizeObserver.observe(el);

      // Re-apply Canvas theme tokens whenever data-theme changes between light and dark.
      themeObserver = new MutationObserver(() => {
        const nextTokens = readThemeTokens();
        tokensRef.current = nextTokens;

        chart.applyOptions({
          layout: {
            background: { color: nextTokens.bg },
            textColor: nextTokens.inkMuted,
          },
          grid: {
            vertLines: { color: nextTokens.line },
            horzLines: { color: nextTokens.line },
          },
          rightPriceScale: { borderColor: nextTokens.line },
          timeScale: { borderColor: nextTokens.line },
        });

        candleSeries.applyOptions({
          upColor: nextTokens.up,
          downColor: nextTokens.down,
          borderUpColor: nextTokens.up,
          borderDownColor: nextTokens.down,
          wickUpColor: nextTokens.up,
          wickDownColor: nextTokens.down,
        });
      });
      themeObserver.observe(document.documentElement, {
        attributes: true,
        attributeFilter: ["data-theme"],
      });

      chartRef.current = chart;
      candleSeriesRef.current = candleSeries;
      volumeSeriesRef.current = volumeSeries;
      markerPluginRef.current = markerPlugin;
      setReady(true);
    }

    mountChart();

    return () => {
      disposed = true;
      resizeObserver?.disconnect();
      themeObserver?.disconnect();
      markerPluginRef.current?.detach();
      chartRef.current?.remove();
      chartRef.current = null;
      candleSeriesRef.current = null;
      volumeSeriesRef.current = null;
      markerPluginRef.current = null;
    };
  }, []);

  // Replace chart dataset cleanly whenever symbol, timeframe, or initial candles change.
  useEffect(() => {
    const chart = chartRef.current;
    const candleSeries = candleSeriesRef.current;
    const volumeSeries = volumeSeriesRef.current;
    if (!ready || !chart || !candleSeries || !volumeSeries) {
      return;
    }

    const tokens = tokensRef.current;

    candleSeries.applyOptions({
      priceFormat: {
        type: "price",
        precision: digits,
        minMove: Number((1 / Math.pow(10, digits)).toFixed(digits)),
      },
    });

    const candleData: CandlestickData<Time>[] = candles.map((bar) => ({
      time: bar.time as UTCTimestamp,
      open: bar.open,
      high: bar.high,
      low: bar.low,
      close: bar.close,
    }));

    const volumeData: HistogramData<Time>[] = candles.map((bar) => ({
      time: bar.time as UTCTimestamp,
      value: bar.volume,
      color: bar.close >= bar.open ? tokens.volUp : tokens.volDown,
    }));

    candleSeries.setData(candleData);
    volumeSeries.setData(volumeData);

    const latest = candles.at(-1);
    const market = `${symbol}/${timeframe}`;
    if (latest && market !== fittedMarket.current) {
      chart.timeScale().fitContent();
      fittedMarket.current = market;
    }
    lastBarTime.current = latest?.time ?? 0;
    setHoverBar(latest ? { ...latest } : null);
  }, [ready, symbol, timeframe, digits, candles]);

  // Synchronize editorial news markers aligned onto candlestick buckets.
  useEffect(() => {
    const markerPlugin = markerPluginRef.current;
    if (!ready || !markerPlugin) {
      return;
    }

    const tokens = tokensRef.current;

    // Group multiple articles matching the same candlestick bar into one clear marker indicator.
    const countsByTime = new Map<number, number>();
    const firstByTime = new Map<number, MarketNewsMarker>();

    for (const item of markers) {
      countsByTime.set(item.time, (countsByTime.get(item.time) ?? 0) + 1);
      if (!firstByTime.has(item.time)) {
        firstByTime.set(item.time, item);
      }
    }

    const barMarkers: SeriesMarker<Time>[] = Array.from(firstByTime.entries())
      .sort(([a], [b]) => a - b)
      .map(([t, item]) => {
        const count = countsByTime.get(t) ?? 1;
        return {
          time: t as UTCTimestamp,
          position: "belowBar",
          shape: "circle",
          color: tokens.amber,
          text: count > 1 ? `闻×${count}` : `${Math.round(item.score)}`,
        };
      });

    markerPlugin.setMarkers(barMarkers);
  }, [ready, markers]);

  const activeBar = hoverBar ?? candles.at(-1) ?? null;
  const isUp = activeBar ? activeBar.close >= activeBar.open : true;

  return (
    <div className="relative w-full">
      {/* Live OHLCV crosshair readout bar */}
      <div className="mb-2 flex flex-wrap items-center gap-x-4 gap-y-1 px-1 text-[11.5px] mono">
        {activeBar ? (
          <>
            <span className="text-ink-4">{formatAxisTime(activeBar.time, timeframe)}</span>
            <span className="text-ink-3">
              开 <strong className="font-semibold text-ink">{activeBar.open.toFixed(digits)}</strong>
            </span>
            <span className="text-ink-3">
              高 <strong className="font-semibold text-ink">{activeBar.high.toFixed(digits)}</strong>
            </span>
            <span className="text-ink-3">
              低 <strong className="font-semibold text-ink">{activeBar.low.toFixed(digits)}</strong>
            </span>
            <span className="text-ink-3">
              收{" "}
              <strong className={`font-semibold ${isUp ? "text-ok" : "text-hot"}`}>
                {activeBar.close.toFixed(digits)}
              </strong>
            </span>
            <span className="text-ink-4">量 {activeBar.volume.toFixed(0)}</span>
          </>
        ) : (
          <span className="text-ink-4">暂无 K 线样本数据</span>
        )}
      </div>

      {/* Canvas mount container */}
      <div
        ref={containerRef}
        className="relative h-[440px] w-full overflow-hidden rounded-panel border border-line bg-surface"
      >
        {!ready && (
          <div className="flex h-full w-full items-center justify-center text-[13px] text-ink-4">
            正在初始化 K 线图引擎…
          </div>
        )}
        {ready && candles.length === 0 && (
          <div className="pointer-events-none absolute inset-0 flex items-center justify-center text-[13px] text-ink-4">
            当前周期暂无已采集的 K 线数据（开启 MARKET_DATA_ENABLED 后自动生成）
          </div>
        )}
      </div>
    </div>
  );
});
