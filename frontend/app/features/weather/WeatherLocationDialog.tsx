import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { IconClose, IconMapPin } from "../../components/icons";
import { useModal } from "../../components/ui/modal";
import { setWeatherLocationPreference, type WeatherLocationPreference } from "../../lib/local-state";
import { getWeatherJSON, type WeatherLanguage, type WeatherLocation } from "./weatherApi";

interface WeatherLocationDialogProps {
  open: boolean;
  onClose: () => void;
  language: WeatherLanguage;
  currentLocation: WeatherLocationPreference | null;
}

/** Modal dialog allowing manual city search or restoring IP geolocation for daily weather. */
export function WeatherLocationDialog({
  open,
  onClose,
  language,
  currentLocation,
}: WeatherLocationDialogProps) {
  const isEn = language === "en";
  const [queryText, setQueryText] = useState("");
  const [results, setResults] = useState<WeatherLocation[] | null>(null);
  const [pending, setPending] = useState(false);
  const [failed, setFailed] = useState(false);
  const panelRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const requestRef = useRef<AbortController | null>(null);
  const titleId = useId();

  useModal({
    open,
    panel: panelRef,
    onClose,
    initialFocus: inputRef,
  });

  // Reset transient search state when closing and abort pending requests on unmount
  useEffect(() => {
    if (!open) {
      requestRef.current?.abort();
      setQueryText("");
      setResults(null);
      setPending(false);
      setFailed(false);
    }

    return () => {
      requestRef.current?.abort();
    };
  }, [open]);

  if (!open) {
    return null;
  }

  /** Query matching cities from backend weather search endpoint. */
  async function handleSearch(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const trimmed = queryText.trim();
    if (!trimmed) {
      return;
    }

    requestRef.current?.abort();
    const controller = new AbortController();
    requestRef.current = controller;
    setPending(true);
    setFailed(false);
    setResults(null);

    try {
      const params = new URLSearchParams({
        q: trimmed,
        lang: language,
      });
      const matches = await getWeatherJSON<WeatherLocation[]>(
        "/api/me/weather/locations",
        params,
        controller.signal,
      );

      if (!controller.signal.aborted) {
        setResults(matches);
      }
    } catch {
      if (!controller.signal.aborted) {
        setFailed(true);
      }
    } finally {
      if (!controller.signal.aborted) {
        setPending(false);
      }
    }
  }

  /** Persist chosen city locally and dismiss the modal. */
  function handlePick(loc: WeatherLocation) {
    setWeatherLocationPreference({
      label: loc.label,
      latitude: loc.latitude,
      longitude: loc.longitude,
    });
    onClose();
  }

  /** Clear manual selection so daily weather falls back to visitor IP estimation. */
  function handleResetToIp() {
    setWeatherLocationPreference(null);
    onClose();
  }

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4 backdrop-blur-xs"
      onClick={(e) => {
        if (e.target === e.currentTarget) {
          onClose();
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
              {isEn ? "Weather Location" : "修改天气地理位置"}
            </h2>
            <p className="mt-1 text-[12.5px] text-ink-3">
              {isEn
                ? "Displayed in Daily Brief. Defaults to IP estimation when cleared."
                : "设置后将展示在「我的日报」顶部，清空则恢复按请求 IP 判断。"}
            </p>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label={isEn ? "Close" : "关闭"}
            className="grid size-8 shrink-0 place-items-center rounded-full text-ink-4 transition-colors hover:bg-bg-sunk hover:text-ink"
          >
            <IconClose size={16} />
          </button>
        </div>

        <form onSubmit={handleSearch} className="mt-4 flex items-center gap-2">
          <input
            ref={inputRef}
            type="search"
            value={queryText}
            onChange={(e) => setQueryText(e.currentTarget.value)}
            maxLength={80}
            required
            aria-label={isEn ? "City name" : "城市名称"}
            placeholder={isEn ? "Enter city name (e.g. Beijing, Tokyo)" : "输入国内或海外城市名"}
            className="h-10 min-w-0 flex-1 rounded-control border border-line-strong bg-bg-sunk px-3 text-[13.5px] text-ink outline-none transition-colors placeholder:text-ink-4 focus:border-accent"
          />
          <button
            type="submit"
            disabled={pending || !queryText.trim()}
            className="h-10 shrink-0 rounded-control bg-accent px-4 text-[13px] font-semibold text-accent-contrast transition-colors hover:bg-accent-ink disabled:opacity-45"
          >
            {isEn ? "Search" : "搜索"}
          </button>
        </form>

        <div role="status" className="text-[12.5px] text-ink-3">
          {pending && <p className="mt-3">{isEn ? "Searching…" : "正在搜索…"}</p>}
          {failed && (
            <p className="mt-3 text-hot">
              {isEn ? "City search is temporarily unavailable." : "城市搜索暂不可用。"}
            </p>
          )}
          {results?.length === 0 && (
            <p className="mt-3">{isEn ? "No matching cities found." : "未找到匹配的城市。"}</p>
          )}
        </div>

        {results && results.length > 0 && (
          <ul className="mt-3 max-h-[220px] divide-y divide-line overflow-y-auto rounded-control border border-line bg-bg-sunk/40 scrollbar-thin">
            {results.map((item, idx) => (
              <li key={`${item.latitude},${item.longitude},${idx}`}>
                <button
                  type="button"
                  onClick={() => handlePick(item)}
                  className="flex w-full items-center gap-2 px-3 py-2.5 text-left text-[13px] text-ink transition-colors hover:bg-bg-sunk hover:text-accent"
                >
                  <IconMapPin size={14} className="shrink-0 text-ink-4" />
                  <span className="truncate">{item.label}</span>
                </button>
              </li>
            ))}
          </ul>
        )}

        <div className="mt-5 flex items-center justify-between gap-3 border-t border-line pt-3">
          {currentLocation ? (
            <button
              type="button"
              onClick={handleResetToIp}
              className="text-[12.5px] font-medium text-accent hover:underline"
            >
              {isEn ? "Use IP estimate" : "恢复按 IP 自动判断"}
            </button>
          ) : (
            <a
              href="https://www.qweather.com"
              target="_blank"
              rel="noopener noreferrer"
              className="text-[11px] text-ink-4 hover:underline"
            >
              {isEn ? "Location search by QWeather" : "城市数据由和风天气提供"}
            </a>
          )}

          <button
            type="button"
            onClick={onClose}
            className="rounded-full border border-line px-4 py-1.5 text-[12.5px] font-medium text-ink-2 transition-colors hover:bg-bg-sunk"
          >
            {isEn ? "Cancel" : "取消"}
          </button>
        </div>
      </div>
    </div>
  );
}
