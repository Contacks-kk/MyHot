import { useOutletContext, useRouteLoaderData } from "react-router";

export interface Catalog {
  subject: string;
  releaseUnit?: string | null;
  categories: Array<{
    key: string;
    label: string;
    publicKey: string;
    feedLabel: string;
  }>;
  topicGroups: Array<{
    key: string;
    name: string;
    blurb: string;
  }>;
  topics: Array<{
    slug: string;
    name: string;
    group: string;
    definition: string;
  }>;
}

/** Read the display catalog loaded by the admin layout. */
export function useAdminCatalog(): Catalog {
  return useOutletContext<Catalog>();
}

/** Read the catalog loaded for the verified user session. */
export function useUserCatalog(): Catalog {
  const root = useRouteLoaderData<{ catalog: Catalog | null }>("root");
  if (!root?.catalog) {
    throw new Error("用户目录暂时不可用");
  }
  return root.catalog;
}
