import { useEffect, useRef, useState } from "react";
import { IconChevronLeft, IconChevronRight, IconClose } from "../icons";

interface CalendarPopoverProps {
  isOpen: boolean;
  onClose: () => void;
  selectedDate: string;
  today: string;
  locale?: "zh" | "en";
  onSelectDate: (dateStr: string) => void;
}

interface CalendarCell {
  dateStr: string;
  day: number;
  isCurrentMonth: boolean;
  isToday: boolean;
  isSelected: boolean;
  isDisabled: boolean;
}

/** Offset a Beijing date string by a given number of days. */
function offsetDate(baseDateStr: string, offsetDays: number): string {
  const d = new Date(`${baseDateStr}T12:00:00+08:00`);
  d.setDate(d.getDate() + offsetDays);

  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");

  return `${y}-${m}-${day}`;
}

/** Parse a YYYY-MM-DD string into year, month (0-indexed), and day numbers. */
function parseDateParts(dateStr: string): { year: number; month: number; day: number } {
  const parts = dateStr.split("-").map(Number);
  if (parts.length === 3 && !Number.isNaN(parts[0]) && !Number.isNaN(parts[1]) && !Number.isNaN(parts[2])) {
    return {
      year: parts[0]!,
      month: parts[1]! - 1,
      day: parts[2]!,
    };
  }

  const fallback = new Date();
  return {
    year: fallback.getFullYear(),
    month: fallback.getMonth(),
    day: fallback.getDate(),
  };
}

/** Format a year and month number into localized header title. */
function formatMonthTitle(year: number, month: number, locale: "zh" | "en"): string {
  if (locale === "en") {
    const monthNames = [
      "January",
      "February",
      "March",
      "April",
      "May",
      "June",
      "July",
      "August",
      "September",
      "October",
      "November",
      "December",
    ];
    return `${monthNames[month]} ${year}`;
  }

  return `${year}年 ${month + 1}月`;
}

/** Render a customized, warm paper styled calendar dropdown popover for date selection. */
export function CalendarPopover({
  isOpen,
  onClose,
  selectedDate,
  today,
  locale = "zh",
  onSelectDate,
}: CalendarPopoverProps) {
  const popoverRef = useRef<HTMLDivElement>(null);
  const isEn = locale === "en";

  // Parse initial view year and month based on selected date or today
  const initial = parseDateParts(selectedDate || today);
  const [viewYear, setViewYear] = useState(initial.year);
  const [viewMonth, setViewMonth] = useState(initial.month);

  const todayParts = parseDateParts(today);
  const isMaxMonth = viewYear > todayParts.year || (viewYear === todayParts.year && viewMonth >= todayParts.month);

  // Sync viewed year/month when selectedDate changes or modal opens
  useEffect(() => {
    if (isOpen) {
      const target = parseDateParts(selectedDate || today);
      setViewYear(target.year);
      setViewMonth(target.month);
    }
  }, [isOpen, selectedDate, today]);

  // Click outside and escape key handling
  useEffect(() => {
    if (!isOpen) {
      return;
    }

    /** Handle global mousedown to dismiss popover when clicking outside. */
    function handleClickOutside(e: MouseEvent) {
      if (popoverRef.current && !popoverRef.current.contains(e.target as Node)) {
        onClose();
      }
    }

    /** Handle escape key to dismiss popover. */
    function handleKeyDown(e: KeyboardEvent) {
      if (e.key === "Escape") {
        onClose();
      }
    }

    document.addEventListener("mousedown", handleClickOutside);
    document.addEventListener("keydown", handleKeyDown);

    return () => {
      document.removeEventListener("mousedown", handleClickOutside);
      document.removeEventListener("keydown", handleKeyDown);
    };
  }, [isOpen, onClose]);

  if (!isOpen) {
    return null;
  }

  /** Navigate viewing calendar to previous month. */
  function handlePrevMonth() {
    if (viewMonth === 0) {
      setViewYear((y) => y - 1);
      setViewMonth(11);
    } else {
      setViewMonth((m) => m - 1);
    }
  }

  /** Navigate viewing calendar to next month. */
  function handleNextMonth() {
    if (isMaxMonth) {
      return;
    }

    if (viewMonth === 11) {
      setViewYear((y) => y + 1);
      setViewMonth(0);
    } else {
      setViewMonth((m) => m + 1);
    }
  }

  // Generate 7x6 day grid for current view month
  const firstDayOfWeek = new Date(viewYear, viewMonth, 1).getDay();
  const startCol = firstDayOfWeek === 0 ? 6 : firstDayOfWeek - 1;
  const daysInCurrentMonth = new Date(viewYear, viewMonth + 1, 0).getDate();
  const daysInPrevMonth = new Date(viewYear, viewMonth, 0).getDate();

  const cells: CalendarCell[] = [];

  // Trailing days from previous month
  for (let i = startCol - 1; i >= 0; i--) {
    const d = daysInPrevMonth - i;
    const prevDate = new Date(viewYear, viewMonth - 1, d);
    const py = prevDate.getFullYear();
    const pm = String(prevDate.getMonth() + 1).padStart(2, "0");
    const pday = String(d).padStart(2, "0");
    const dateStr = `${py}-${pm}-${pday}`;

    cells.push({
      dateStr,
      day: d,
      isCurrentMonth: false,
      isToday: dateStr === today,
      isSelected: dateStr === selectedDate,
      isDisabled: true,
    });
  }

  // Days in current month
  for (let d = 1; d <= daysInCurrentMonth; d++) {
    const cm = String(viewMonth + 1).padStart(2, "0");
    const cday = String(d).padStart(2, "0");
    const dateStr = `${viewYear}-${cm}-${cday}`;
    const isFuture = dateStr > today;

    cells.push({
      dateStr,
      day: d,
      isCurrentMonth: true,
      isToday: dateStr === today,
      isSelected: dateStr === selectedDate,
      isDisabled: isFuture,
    });
  }

  // Leading days from next month to fill grid
  const remainingCells = (7 - (cells.length % 7)) % 7;
  for (let d = 1; d <= remainingCells; d++) {
    const nextDate = new Date(viewYear, viewMonth + 1, d);
    const ny = nextDate.getFullYear();
    const nm = String(nextDate.getMonth() + 1).padStart(2, "0");
    const nday = String(d).padStart(2, "0");
    const dateStr = `${ny}-${nm}-${nday}`;

    cells.push({
      dateStr,
      day: d,
      isCurrentMonth: false,
      isToday: dateStr === today,
      isSelected: dateStr === selectedDate,
      isDisabled: true,
    });
  }

  const weekLabels = isEn
    ? ["Mo", "Tu", "We", "Th", "Fr", "Sa", "Su"]
    : ["一", "二", "三", "四", "五", "六", "日"];

  const yesterday = offsetDate(today, -1);
  const dayBeforeYesterday = offsetDate(today, -2);

  return (
    <div
      ref={popoverRef}
      role="dialog"
      aria-label={isEn ? "Calendar date picker" : "日历日期选择器"}
      className="absolute right-0 top-full mt-2 z-50 w-[300px] rounded-card border border-line bg-surface p-4 shadow-xl transition-all select-none"
    >
      {/* Calendar Header with Month Navigation */}
      <div className="flex items-center justify-between pb-3 border-b border-line/70">
        <span className="text-[13.5px] font-semibold text-ink">
          {formatMonthTitle(viewYear, viewMonth, locale)}
        </span>

        <div className="flex items-center gap-1">
          <button
            type="button"
            onClick={handlePrevMonth}
            aria-label={isEn ? "Previous month" : "上个月"}
            className="flex size-7 items-center justify-center rounded-full text-ink-3 hover:bg-bg-sunk hover:text-ink transition-colors"
          >
            <IconChevronLeft size={15} />
          </button>
          <button
            type="button"
            onClick={handleNextMonth}
            disabled={isMaxMonth}
            aria-label={isEn ? "Next month" : "下个月"}
            className="flex size-7 items-center justify-center rounded-full text-ink-3 hover:bg-bg-sunk hover:text-ink transition-colors disabled:opacity-20 disabled:cursor-not-allowed"
          >
            <IconChevronRight size={15} />
          </button>
        </div>
      </div>

      {/* Weekday Row */}
      <div className="grid grid-cols-7 pt-2.5 pb-1 text-center text-[11px] font-medium text-ink-4">
        {weekLabels.map((lbl) => (
          <span key={lbl}>{lbl}</span>
        ))}
      </div>

      {/* Days Grid */}
      <div className="grid grid-cols-7 gap-1 pt-1">
        {cells.map((cell) => {
          if (!cell.isCurrentMonth) {
            return (
              <div
                key={cell.dateStr}
                className="flex size-8 items-center justify-center text-[11.5px] text-ink-4/30 cursor-default"
              >
                {cell.day}
              </div>
            );
          }

          if (cell.isDisabled) {
            return (
              <div
                key={cell.dateStr}
                className="flex size-8 items-center justify-center text-[11.5px] text-ink-4/25 cursor-not-allowed"
              >
                {cell.day}
              </div>
            );
          }

          if (cell.isSelected) {
            return (
              <button
                type="button"
                key={cell.dateStr}
                onClick={() => onSelectDate(cell.dateStr)}
                className="flex size-8 items-center justify-center rounded-control bg-accent text-[12px] font-bold text-accent-contrast shadow-xs transition-colors"
              >
                {cell.day}
              </button>
            );
          }

          if (cell.isToday) {
            return (
              <button
                type="button"
                key={cell.dateStr}
                onClick={() => onSelectDate(cell.dateStr)}
                className="flex size-8 items-center justify-center rounded-control border border-accent text-[12px] font-bold text-accent bg-accent-soft/30 hover:bg-accent hover:text-accent-contrast transition-colors"
              >
                {cell.day}
              </button>
            );
          }

          return (
            <button
              type="button"
              key={cell.dateStr}
              onClick={() => onSelectDate(cell.dateStr)}
              className="flex size-8 items-center justify-center rounded-control text-[12px] font-medium text-ink-2 hover:bg-bg-sunk hover:text-accent transition-colors"
            >
              {cell.day}
            </button>
          );
        })}
      </div>

      {/* Shortcuts and Quick Actions Footer */}
      <div className="mt-3.5 flex items-center justify-between border-t border-line/70 pt-2.5 text-[11.5px]">
        <div className="flex items-center gap-1.5">
          <button
            type="button"
            onClick={() => onSelectDate(today)}
            className="rounded-full border border-line px-2.5 py-1 text-ink-3 hover:border-accent hover:text-accent transition-colors"
          >
            {isEn ? "Today" : "今天"}
          </button>
          <button
            type="button"
            onClick={() => onSelectDate(yesterday)}
            className="rounded-full border border-line px-2.5 py-1 text-ink-3 hover:border-accent hover:text-accent transition-colors"
          >
            {isEn ? "Yesterday" : "昨天"}
          </button>
          <button
            type="button"
            onClick={() => onSelectDate(dayBeforeYesterday)}
            className="rounded-full border border-line px-2.5 py-1 text-ink-3 hover:border-accent hover:text-accent transition-colors hidden sm:inline-block"
          >
            {isEn ? "2 days ago" : "前天"}
          </button>
        </div>

        <button
          type="button"
          onClick={onClose}
          className="text-ink-4 hover:text-ink transition-colors px-1 py-1"
        >
          {isEn ? "Close" : "关闭"}
        </button>
      </div>
    </div>
  );
}
