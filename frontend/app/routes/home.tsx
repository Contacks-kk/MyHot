import { redirect, useLoaderData } from "react-router";
import type { Route } from "./+types/home";
import type { Screen } from "../components/shell/screens";
import { SITE } from "@aihot/site";
import { userApiMe } from "../lib/user-api.server";
import { SpiderLanding } from "../features/home/SpiderLanding";

export const handle: Screen = { tab: "hot", name: "个人热点榜单" };

/** Send authenticated readers to the unified hotspot page and keep the public introduction. */
export async function loader({ request }: Route.LoaderArgs) {
  const user = await userApiMe(request);
  if (user) {
    throw redirect("/hot", { headers: { "Cache-Control": "private, no-store" } });
  }
  const response = await fetch(`${process.env.GO_API_BASE_URL || "http://127.0.0.1:3002"}/api/auth/registration-status`, {
    signal: request.signal,
  }).catch(() => null);
  const status = response?.ok ? ((await response.json()) as { enabled: boolean }) : { enabled: false };
  return { registrationEnabled: status.enabled };
}

export const meta: Route.MetaFunction = () => [{ title: SITE.name }, { name: "robots", content: "noindex, nofollow" }];
export const headers: Route.HeadersFunction = () => ({ "Cache-Control": "private, no-store", "X-Robots-Tag": "noindex, nofollow" });

/** Render the public introduction; signed-in readers use the personal hotspot page. */
export default function Home() {
  const { registrationEnabled } = useLoaderData<typeof loader>();
  return <SpiderLanding registrationEnabled={registrationEnabled} />;
}
