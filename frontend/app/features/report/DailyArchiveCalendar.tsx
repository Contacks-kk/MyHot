import { useEffect, useMemo, useRef, useState } from "react";
import { IconCalendar, IconChevronDown, IconChevronLeft, IconChevronRight, IconClose } from "../../components/icons";
import { Halftone } from "./Halftone";

export interface DailyArchiveHeading {
  id: string;
  date: string;
  title: string;
  createdAt: string;
}

interface DailyArchiveCalendarProps {
  reports: DailyArchiveHeading[];
  currentReportId: string;
  currentDate: string;
  year: string;
  month: number;
  day: string;
  weekday: string;
  open: boolean;
  onOpenChange: (next: boolean) => void;
  onSelectReport: (reportId: string) => void;
}

interface DateParts {
  year: number;
  month: number;
  day: number;
}

interface CalendarDayCell {
  dateStr: string;
  day: number;
  issues: DailyArchiveHeading[];
  isSelected: boolean;
  isInspected: boolean;
}

/** Parse a YYYY-MM-DD date string into zero-indexed month calendar parts. */
function parseDateParts(dateStr: string): DateParts {
  const parts = dateStr.split("-").map(Number);

  // Use explicit numeric fields when the input matches YYYY-MM-DD
  if (parts.length === 3 && !Number.isNaN(parts[0]) && !Number.isNaN(parts[1]) && !Number.isNaN(parts[2])) {
    return {
      year: parts[0]!,
      month: parts[1]! - 1,
      day: parts[2]!,
    };
  }

  const now = new Date();
  return {
    year: now.getUTCFullYear(),
    month: now.getUTCMonth(),
    day: now.getUTCDate(),
  };
}

/** Format a YYYY-MM key from year and zero-indexed month for range comparison. */
function formatMonthKey(year: number, month: number): number {
  return year * 12 + month;
}

/** Render the twin-ring tear-off desk calendar button and anchored past-issue calendar popover. */
export function DailyArchiveCalendar({
  reports,
  currentReportId,
  currentDate,
  year,
  month,
  day,
  weekday,
  open,
  onOpenChange,
  onSelectReport,
}: DailyArchiveCalendarProps) {
  const containerRef = useRef<HTMLDivElement>(null);
  const initialParts = parseDateParts(currentDate);
  const [viewYear, setViewYear] = useState(initialParts.year);
  const [viewMonth, setViewMonth] = useState(initialParts.month);
  const [inspectedDate, setInspectedDate] = useState<string | null>(null);

  // Index issues by YYYY-MM-DD and track earliest/latest available months
  const { issuesByDate, minMonthKey, maxMonthKey } = useMemo(() => {
    const map = new Map<string, DailyArchiveHeading[]>();
    let minKey = formatMonthKey(initialParts.year, initialParts.month);
    let maxKey = minKey;

    for (const item of reports) {
      const existing = map.get(item.date);
      if (existing) {
        existing.push(item);
      } else {
        map.set(item.date, [item]);
      }

      const parts = parseDateParts(item.date);
      const key = formatMonthKey(parts.year, parts.month);
      if (key < minKey) {
        minKey = key;
      }
      if (key > maxKey) {
        maxKey = key;
      }
    }

    return {
      issuesByDate: map,
      minMonthKey: minKey,
      maxMonthKey: maxKey,
    };
  }, [reports, initialParts.year, initialParts.month]);

  // Reset calendar month and inspected day whenever popover opens or issue changes
  useEffect(() => {
    if (open) {
      const parts = parseDateParts(currentDate);
      setViewYear(parts.year);
      setViewMonth(parts.month);
      setInspectedDate(null);
    }
  }, [open, currentDate]);

  // Dismiss popover on outside click or Escape key
  useEffect(() => {
    if (!open) {
      return;
    }

    /** Close popover when clicking outside the calendar anchor container. */
    function handlePointerDown(event: MouseEvent) {
      if (containerRef.current && !containerRef.current.contains(event.target as Node)) {
        onOpenChange(false);
      }
    }

    /** Close popover when pressing Escape. */
    function handleKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") {
        onOpenChange(false);
      }
    }

    document.addEventListener("mousedown", handlePointerDown);
    document.addEventListener("keydown", handleKeyDown);

    return () => {
      document.removeEventListener("mousedown", handlePointerDown);
      document.removeEventListener("keydown", handleKeyDown);
    };
  }, [open, onOpenChange]);

  const currentMonthKey = formatMonthKey(viewYear, viewMonth);
  const canGoPrev = currentMonthKey > minMonthKey;
  const canGoNext = currentMonthKey < maxMonthKey;

  /** Step calendar view backward by one month when older issues exist. */
  function handlePrevMonth() {
    if (!canGoPrev) {
      return;
    }

    if (viewMonth === 0) {
      setViewYear((prev) => prev - 1);
      setViewMonth(11);
    } else {
      setViewMonth((prev) => prev - 1);
    }
  }

  /** Step calendar view forward by one month when newer issues exist. */
  function handleNextMonth() {
    if (!canGoNext) {
      return;
    }

    if (viewMonth === 11) {
      setViewYear((prev) => prev + 1);
      setViewMonth(0);
    } else {
      setViewMonth((prev) => prev + 1);
    }
  }

  // Compute Monday-first month grid using UTC dates so SSR and client never disagree
  const firstDayUtc = new Date(Date.UTC(viewYear, viewMonth, 1)).getUTCDay();
  const leadingEmptyCount = firstDayUtc === 0 ? 6 : firstDayUtc - 1;
  const daysInMonth = new Date(Date.UTC(viewYear, viewMonth + 1, 0)).getUTCDate();
  const trailingEmptyCount = (7 - ((leadingEmptyCount + daysInMonth) % 7)) % 7;

  const dayCells: CalendarDayCell[] = [];
  const monthText = String(viewMonth + 1).padStart(2, "0");

  for (let d = 1; d <= daysInMonth; d += 1) {
    const dayText = String(d).padStart(2, "0");
    const dateStr = `${viewYear}-${monthText}-${dayText}`;
    const issues = issuesByDate.get(dateStr) ?? [];

    dayCells.push({
      dateStr,
      day: d,
      issues,
      isSelected: dateStr === currentDate,
      isInspected: dateStr === inspectedDate,
    });
  }

  // Determine the next older issue day number to peek subtly behind the curled corner
  const currentIdx = reports.findIndex((item) => item.id === currentReportId);
  const olderIssue = currentIdx >= 0 ? reports[currentIdx + 1] : undefined;
  const peekDay = olderIssue ? String(Number(parseDateParts(olderIssue.date).day)) : String(Math.max(1, Number(day) - 1));

  const previewDate = inspectedDate ?? currentDate;
  const previewIssues = issuesByDate.get(previewDate) ?? [];
  const latestIssue = reports[0];
  const hasMultipleMonths = maxMonthKey > minMonthKey;

  /** Select a date in the grid: navigate directly when unique, or list all same-day issues. */
  function handleDayClick(cell: CalendarDayCell) {
    if (cell.issues.length === 0) {
      return;
    }

    if (cell.issues.length === 1) {
      const target = cell.issues[0]!;
      onOpenChange(false);
      onSelectReport(target.id);
      return;
    }

    setInspectedDate(cell.dateStr);
  }

  return (
    <div ref={containerRef} className="relative shrink-0">
      {/* Physical twin-ring binder bridges */}
      <div className="daily-desk-rings" aria-hidden="true">
        <span className="daily-desk-ring" />
        <span className="daily-desk-ring" />
      </div>

      <button
        type="button"
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-label={`选择往期日报，当前 ${year} 年 ${month} 月 ${Number(day)} 日`}
        onClick={() => onOpenChange(!open)}
        className="daily-desk-card group block w-[126px] cursor-pointer p-0 sm:w-[152px]"
      >
        <div className="daily-desk-band px-2.5 pb-1.5 pt-3 sm:px-3 sm:pt-3.5">
          <div className="daily-desk-holes" aria-hidden="true">
            <span className="daily-desk-hole" />
            <span className="daily-desk-hole" />
          </div>
          <span className="block whitespace-nowrap text-[11px] font-bold tracking-[0.06em]">
            {year} 年 {month} 月
          </span>
        </div>

        <div className="daily-desk-sheet px-2.5 pb-2.5 pt-2.5 sm:px-4 sm:pb-3 sm:pt-3">
          <span className="daily-desk-peek num" aria-hidden="true">
            {peekDay}
          </span>

          <Halftone
            seed={`personal-daily-${currentDate}`}
            className="num block text-[42px] font-black leading-none text-ink sm:text-[52px]"
          >
            {day}
          </Halftone>

          <p className="mt-1.5 whitespace-nowrap text-[11.5px] font-semibold text-ink-3">
            {weekday}
          </p>

          <div
            className={`mt-2 inline-flex items-center justify-center gap-1 whitespace-nowrap rounded-full border px-2.5 py-0.5 text-[10.5px] font-semibold transition-colors ${
              open
                ? "border-accent bg-accent-soft text-accent"
                : "border-line-strong bg-surface/80 text-ink-2 group-hover:border-accent group-hover:bg-accent-soft group-hover:text-accent"
            }`}
          >
            <IconCalendar size={11} />
            <span>往期日历</span>
            <IconChevronDown size={11} />
          </div>

          <span className="daily-desk-curl" aria-hidden="true" />
        </div>
      </button>

      {open && (
        <div
          role="dialog"
          aria-label="往期日报日历"
          className="absolute right-0 top-full z-50 mt-3 w-[min(320px,calc(100vw-32px))] rounded-card border border-line-strong bg-surface p-4 shadow-[var(--shadow-pop)] select-none"
        >
          <div className="flex items-center justify-between border-b border-line pb-2.5">
            <span className="text-[13.5px] font-bold text-ink">
              {viewYear} 年 {viewMonth + 1} 月
            </span>

            <div className="flex items-center gap-1">
              {hasMultipleMonths && (
                <>
                  <button
                    type="button"
                    onClick={handlePrevMonth}
                    disabled={!canGoPrev}
                    aria-label="上个月"
                    className="grid size-7 place-items-center rounded-full text-ink-3 transition-colors hover:bg-bg-sunk hover:text-ink disabled:cursor-not-allowed disabled:opacity-25"
                  >
                    <IconChevronLeft size={14} />
                  </button>
                  <button
                    type="button"
                    onClick={handleNextMonth}
                    disabled={!canGoNext}
                    aria-label="下个月"
                    className="grid size-7 place-items-center rounded-full text-ink-3 transition-colors hover:bg-bg-sunk hover:text-ink disabled:cursor-not-allowed disabled:opacity-25"
                  >
                    <IconChevronRight size={14} />
                  </button>
                </>
              )}
              <button
                type="button"
                onClick={() => onOpenChange(false)}
                aria-label="关闭"
                className="grid size-7 place-items-center rounded-full text-ink-3 transition-colors hover:bg-bg-sunk hover:text-ink"
              >
                <IconClose size={14} />
              </button>
            </div>
          </div>

          <div className="grid grid-cols-7 gap-1 pt-2.5 pb-1 text-center text-[11px] font-medium text-ink-4">
            {["一", "二", "三", "四", "五", "六", "日"].map((label) => (
              <span key={label}>{label}</span>
            ))}
          </div>

          <div className="grid grid-cols-7 gap-1 pt-0.5 text-center">
            {Array.from({ length: leadingEmptyCount }).map((_, idx) => (
              <div key={`lead-${idx}`} className="h-9" aria-hidden="true" />
            ))}

            {dayCells.map((cell) => {
              const hasIssue = cell.issues.length > 0;
              if (!hasIssue) {
                return (
                  <div
                    key={cell.dateStr}
                    className="flex h-9 items-center justify-center text-[12px] text-ink-4/40"
                  >
                    {cell.day}
                  </div>
                );
              }

              const hasScheduled = cell.issues.some((item) => !item.id.startsWith("manual-"));
              const hasManual = cell.issues.some((item) => item.id.startsWith("manual-"));
              const highlighted = cell.isInspected || (!inspectedDate && cell.isSelected);

              return (
                <button
                  key={cell.dateStr}
                  type="button"
                  onClick={() => handleDayClick(cell)}
                  title={cell.issues[0]?.title}
                  className={`flex h-9 flex-col items-center justify-center rounded-control border text-[12px] font-bold transition-colors ${
                    highlighted
                      ? "border-accent bg-accent text-accent-contrast"
                      : cell.isSelected
                      ? "border-accent/50 bg-accent-soft text-accent"
                      : "border-transparent bg-bg-sunk text-ink hover:border-accent hover:text-accent"
                  }`}
                >
                  <span className="num leading-none">{cell.day}</span>
                  <span className="mt-1 flex items-center gap-0.5" aria-hidden="true">
                    {hasScheduled && (
                      <span
                        className={`size-1 rounded-full ${
                          highlighted ? "bg-accent-contrast" : "bg-accent"
                        }`}
                      />
                    )}
                    {hasManual && (
                      <span
                        className={`size-1 rounded-full ${
                          highlighted ? "bg-accent-contrast" : "bg-clay"
                        }`}
                      />
                    )}
                  </span>
                </button>
              );
            })}

            {Array.from({ length: trailingEmptyCount }).map((_, idx) => (
              <div key={`trail-${idx}`} className="h-9" aria-hidden="true" />
            ))}
          </div>

          <div className="mt-3 border-t border-line pt-2.5 text-[11.5px]">
            <div className="flex items-center justify-between gap-2 text-ink-4">
              <span>{previewDate}</span>
              <span className="flex items-center gap-2.5">
                <span className="inline-flex items-center gap-1">
                  <span className="size-1.5 rounded-full bg-accent" />
                  定时
                </span>
                <span className="inline-flex items-center gap-1">
                  <span className="size-1.5 rounded-full bg-clay" />
                  手动
                </span>
              </span>
            </div>

            {previewIssues.length > 0 && (
              <div className="mt-2 max-h-[140px] space-y-1.5 overflow-y-auto scrollbar-thin">
                {previewIssues.map((item) => {
                  const isCurrent = item.id === currentReportId;
                  const isManual = item.id.startsWith("manual-");
                  const manualTime = isManual
                    ? new Date(item.createdAt).toLocaleTimeString("zh-CN", {
                        timeZone: "Asia/Shanghai",
                        hour: "2-digit",
                        minute: "2-digit",
                      })
                    : "";

                  return (
                    <button
                      key={item.id}
                      type="button"
                      onClick={() => {
                        onOpenChange(false);
                        onSelectReport(item.id);
                      }}
                      className={`flex w-full items-center justify-between gap-2 rounded-control border px-2.5 py-2 text-left transition-colors ${
                        isCurrent
                          ? "border-accent/40 bg-accent-soft text-ink"
                          : "border-line bg-bg-sunk text-ink-2 hover:border-accent"
                      }`}
                    >
                      <span className="min-w-0 truncate font-medium">{item.title}</span>
                      {isManual ? (
                        <span className="shrink-0 rounded-full bg-clay-soft px-2 py-0.5 text-[10px] font-semibold text-clay">
                          手动 {manualTime}
                        </span>
                      ) : isCurrent ? (
                        <span className="shrink-0 text-[10.5px] font-semibold text-accent">
                          当前
                        </span>
                      ) : (
                        <span className="shrink-0 text-[10.5px] text-ink-4">
                          查看
                        </span>
                      )}
                    </button>
                  );
                })}
              </div>
            )}

            {latestIssue && latestIssue.id !== currentReportId && (
              <button
                type="button"
                onClick={() => {
                  onOpenChange(false);
                  onSelectReport(latestIssue.id);
                }}
                className="mt-2.5 w-full rounded-full border border-line py-1.5 text-center text-[11.5px] font-semibold text-accent transition-colors hover:border-accent hover:bg-accent-softer"
              >
                回到最新一期（{latestIssue.date}）
              </button>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
