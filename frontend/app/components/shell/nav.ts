// Site navigation in one place: the desktop sidebar's sections and the phone tab bar's tabs, the engine's
// and the site's modules'.
import type { ReactNode } from "react";
import { webModules } from "../../site-modules";
import { getDict, type Locale } from "../../lib/i18n";
import {
  IconBolt, IconDoc, IconFlame, IconGrid, IconHeart, IconHistory, IconPlug, IconRss, IconUser,
} from "../icons";

export interface NavItem {
  to: string;
  label: string;
  icon: (p: { size?: number }) => ReactNode;
  /** Match the path exactly (the home page). */
  end?: boolean;
  /** Shows the unread dot while the changelog has news. */
  changelog?: boolean;
}

/** Get localized navigation sections for the desktop sidebar. */
function getSections(locale: Locale): Array<{ title: string; items: NavItem[] }> {
  const dict = getDict(locale);
  return [
    {
      title: dict.nav.workspace,
      items: [
        { to: "/", label: dict.nav.featured, icon: IconBolt, end: true },
        { to: "/hot", label: dict.nav.hot, icon: IconFlame },
        { to: "/daily", label: dict.nav.daily, icon: IconDoc, end: true },
        { to: "/model", label: dict.nav.model, icon: IconPlug },
        { to: "/interests", label: dict.nav.interests, icon: IconGrid },
        { to: "/sources", label: dict.nav.sources, icon: IconRss },
      ],
    },
    {
      title: dict.nav.historyAndAbout,
      items: [
        { to: "/history", label: dict.nav.history, icon: IconHistory },
        { to: "/about", label: dict.nav.about, icon: IconHeart },
        { to: "/more", label: dict.nav.account, icon: IconUser },
      ],
    },
  ];
}

/**
 * The sidebar: the engine's sections with the modules' between 内容 and 更多; a module naming a section
 * that is already there adds to it.
 */
export function sidebar(locale: Locale = "zh"): Array<{ title: string; items: NavItem[] }> {
  const [content, ...rest] = getSections(locale);
  const more = rest.pop()!;
  const sections = [content!, ...rest].map((s) => ({ ...s, items: [...s.items] }));
  for (const m of webModules()) {
    if (!m.sidebar) continue;
    const section = sections.find((s) => s.title === m.sidebar!.section);
    if (section) section.items.push(...m.sidebar.items);
    else sections.push({ title: m.sidebar.section, items: [...m.sidebar.items] });
  }
  return [...sections, more];
}

/** A sidebar entry is lit on its pages; 日报 also covers weekly and monthly reports. */
export function sidebarIsActive(item: NavItem, pathname: string): boolean {
  if (item.end) return pathname === item.to;
  return pathname === item.to || pathname.startsWith(`${item.to}/`);
}

/**
 * The phone tab bar: 全部 lives beside 精选 as a switch, 热点 and 日报 are tabs, and "我的" at /more holds
 * 收藏, 外观, the tools and the site's own pages. Which tab a page sits under is declared by the page
 * itself (components/shell/screens.ts).
 */
export type TabKey =
  | "featured"
  | "hot"
  | "daily"
  | "me"
  // A module's tab.
  | (string & {});

export interface Tab {
  key: TabKey;
  to: string;
  label: string;
  icon: (p: { size?: number }) => ReactNode;
  changelog?: boolean;
}

/** The tab bar: the engine's, the modules' before 我的. */
export function tabs(locale: Locale = "zh"): Tab[] {
  const dict = getDict(locale);
  const engineTabs: Tab[] = [
    { key: "featured", to: "/", label: dict.nav.featured, icon: IconBolt },
    { key: "hot", to: "/hot", label: dict.nav.hot, icon: IconFlame },
    { key: "daily", to: "/daily", label: dict.nav.daily, icon: IconDoc },
    { key: "me", to: "/more", label: dict.nav.account, icon: IconUser, changelog: true },
  ];
  return [...engineTabs.slice(0, -1), ...webModules().flatMap((m) => m.tabs ?? []), engineTabs.at(-1)!];
}
