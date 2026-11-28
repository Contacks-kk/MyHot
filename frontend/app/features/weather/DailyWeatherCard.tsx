import { useEffect, useState } from "react";
import { Link } from "react-router";
import { IconCloudSun, IconMapPin, IconSun } from "../../components/icons";
import { useWeatherLocationPreference } from "../../lib/local-state";
import { getWeatherJSON, type WeatherLanguage, type WeatherView } from "./weatherApi";

/** Determine whether the condition description represents sunny/clear skies. */
function isClearCondition(condition: string): boolean {
  const normalized = condition.toLowerCase();
  return normalized.includes("晴") || normalized.includes("sun") || normalized.includes("clear");
}

/** Render today's forecast for the daily masthead using IP geolocation or user-selected city. */
export function DailyWeatherCard({ language }: { language: WeatherLanguage }) {
  const selected = useWeatherLocationPreference();
  const [weather, setWeather] = useState<WeatherView | null>(null);
  const [unavailable, setUnavailable] = useState(false);
  const [dayVersion, setDayVersion] = useState(0);
  const isEn = language === "en";

  // Fetch today's weather whenever language, manual selection, or forecast day boundary changes
  useEffect(() => {
    const request = new AbortController();
    const query = new URLSearchParams({ lang: language });

    if (selected) {
      query.set("lat", String(selected.latitude));
      query.set("lon", String(selected.longitude));
    }

    setUnavailable(false);
    setWeather(null);

    void getWeatherJSON<WeatherView>("/api/me/weather", query, request.signal)
      .then((view) => {
        if (!request.signal.aborted) {
          setWeather(view);
        }
      })
      .catch(() => {
        if (!request.signal.aborted) {
          setUnavailable(true);
        }
      });

    return () => {
      request.abort();
    };
  }, [language, selected, dayVersion]);

  // Schedule automatic refresh shortly after the current daily forecast window expires
  useEffect(() => {
    const endsAt = weather?.forecast?.endsAt;
    if (!endsAt) {
      return;
    }

    const remaining = Math.max(0, Date.parse(endsAt) - Date.now());
    const timer = window.setTimeout(() => {
      setDayVersion((prev) => prev + 1);
    }, remaining + 500);

    return () => {
      window.clearTimeout(timer);
    };
  }, [weather?.forecast?.endsAt]);

  if (!unavailable && weather && !weather.enabled && !selected) {
    return null;
  }

  if (!weather && !unavailable) {
    return null;
  }

  const forecast = weather?.forecast;
  const cityLabel = selected?.label ?? weather?.location;
  const modeLabel = selected ? (isEn ? "Manual" : "手动设置") : (isEn ? "IP estimate" : "IP 定位");
  const showEstimatedCredits = !selected && Boolean(weather?.estimated);

  // Render empty-location prompt when IP geolocation could not resolve a city on an enabled host
  if (!forecast && !unavailable && weather?.enabled && !cityLabel) {
    return (
      <aside
        aria-label={isEn ? "Daily weather" : "今日天气"}
        className="w-full max-w-[400px] rounded-card border border-line-strong bg-surface/90 px-4 py-3 shadow-[var(--shadow-card)]"
      >
        <div className="flex items-center justify-between gap-3">
          <div className="flex min-w-0 items-center gap-2.5">
            <span className="grid size-9 shrink-0 place-items-center rounded-tile bg-bg-sunk text-ink-3">
              <IconMapPin size={17} />
            </span>
            <div className="min-w-0">
              <p className="truncate text-[13px] font-semibold text-ink">
                {isEn ? "Location unavailable via IP" : "暂未识别请求地理位置"}
              </p>
              <p className="mt-0.5 truncate text-[11px] text-ink-4">
                {isEn ? "Set a city manually in Setting" : "可在设置中手动设定天气城市"}
              </p>
            </div>
          </div>
          <Link
            to="/more?location=edit"
            className="shrink-0 whitespace-nowrap rounded-full border border-line-strong bg-bg-sunk px-3 py-1 text-[11.5px] font-semibold text-accent transition-colors hover:border-accent"
          >
            {isEn ? "Modify Location" : "修改定位"}
          </Link>
        </div>
      </aside>
    );
  }

  const WeatherIcon = forecast && isClearCondition(forecast.condition) ? IconSun : IconCloudSun;

  return (
    <aside
      aria-label={isEn ? "Daily weather" : "今日天气"}
      className="w-full max-w-[400px] rounded-card border border-line-strong bg-gradient-to-br from-surface to-bg-sunk px-4 py-3 shadow-[var(--shadow-card)]"
    >
      <div className="flex items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-3">
          <span className="grid size-10 shrink-0 place-items-center rounded-tile bg-accent-soft text-accent">
            <WeatherIcon size={20} />
          </span>
          <div className="min-w-0">
            {forecast ? (
              <>
                <p className="num text-[24px] font-black leading-none tracking-tight text-ink">
                  {Math.round(forecast.temperatureMin)}–{Math.round(forecast.temperatureMax)}°C
                </p>
                <p className="mt-1 truncate text-[12px] font-medium text-ink-2">
                  {isEn ? "Today" : "今日"} · {forecast.condition}
                </p>
              </>
            ) : (
              <p className="text-[12.5px] text-ink-3">
                {isEn ? "Weather unavailable." : "天气暂不可用。"}
              </p>
            )}
          </div>
        </div>

        <div className="flex min-w-0 flex-col items-end text-right">
          {cityLabel && (
            <p className="flex max-w-full items-center gap-1 text-[12.5px] font-semibold text-ink">
              <IconMapPin size={13} className="shrink-0 text-ink-3" />
              <span className="truncate">{cityLabel}</span>
            </p>
          )}
          <span className="mt-1 inline-flex rounded-full border border-line bg-bg-sunk px-2 py-0.5 text-[10px] font-medium text-ink-3">
            {modeLabel}
          </span>
        </div>
      </div>

      <div className="mt-2.5 flex items-center justify-between gap-3 border-t border-dashed border-line pt-1.5 text-[10.5px] text-ink-4">
        <div className="flex flex-wrap items-center gap-x-1.5">
          <a href="https://www.qweather.com" target="_blank" rel="noopener noreferrer" className="hover:underline">
            {isEn ? "QWeather" : "和风天气"}
          </a>
          {showEstimatedCredits && (
            <>
              <span aria-hidden="true">·</span>
              <a href="https://db-ip.com" target="_blank" rel="noopener noreferrer" className="hover:underline">
                DB-IP
              </a>
            </>
          )}
        </div>

        <Link
          to="/more?location=edit"
          className="shrink-0 font-semibold text-accent hover:underline"
        >
          {isEn ? "Modify Location" : "修改定位"}
        </Link>
      </div>
    </aside>
  );
}
