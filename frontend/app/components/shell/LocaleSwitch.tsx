import { useEffect, useState } from "react";
import { setLocalePreference, useLocalePreference, type LocalePreference } from "../../lib/local-state";

type Choice = LocalePreference;

const OPTIONS: Array<{ key: Choice; label: string; fullLabel: string }> = [
  { key: "zh", label: "中", fullLabel: "中文" },
  { key: "en", label: "EN", fullLabel: "English" },
];

/** Two-way language switch (中 / EN) with a sliding thumb, matching the ThemeSwitch aesthetic. */
export function LocaleSwitch({ className = "" }: { className?: string }) {
  const pref = useLocalePreference();
  const [mounted, setMounted] = useState(false);
  useEffect(() => setMounted(true), []);
  const current: Choice = !mounted ? "zh" : pref;
  const index = Math.max(0, OPTIONS.findIndex((o) => o.key === current));

  /** Switch website language preference and notify listeners. */
  const choose = (key: Choice) => {
    setLocalePreference(key);
  };

  return (
    <div
      role="radiogroup"
      aria-label="语言切换"
      className={`relative grid h-[34px] min-w-[76px] grid-cols-2 rounded-full border border-line bg-bg-sunk p-[3px] ${className}`}
    >
      <span
        aria-hidden="true"
        className="absolute inset-y-[3px] left-[3px] w-[calc((100%-6px)/2)] rounded-full border border-line bg-surface shadow-[var(--shadow-card)] transition-transform duration-200 ease-[var(--ease-out-quart)]"
        style={{ transform: `translateX(${index * 100}%)` }}
      />
      {OPTIONS.map((o) => (
        <button
          key={o.key}
          type="button"
          role="radio"
          aria-checked={current === o.key}
          title={o.fullLabel}
          onClick={() => choose(o.key)}
          className={`relative z-10 flex items-center justify-center rounded-full text-[11.5px] font-semibold transition-colors duration-150 ${
            current === o.key ? "text-ink" : "text-ink-4 hover:text-ink-2"
          }`}
        >
          <span>{o.label}</span>
          <span className="sr-only">{o.fullLabel}</span>
        </button>
      ))}
    </div>
  );
}
