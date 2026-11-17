import reportingCatalog from "../../backend-go/config/reporting.json" with { type: "json" };

export const RELEASE: { category: string; tag: string; unit: string } | null = reportingCatalog.release;
export const PLAIN_TERMS: readonly string[] = reportingCatalog.plainTerms;
