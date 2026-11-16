import { credential } from "../config.ts";

/** The Go Worker owns backup execution; the TypeScript alerts still use this to decide if it is expected. */
export function backupConfigured(): boolean {
  const configured = ["DB_BACKUP_STORE_SECRET_ID", "DB_BACKUP_STORE_SECRET_KEY", "DB_BACKUP_STORE_BUCKET", "DB_BACKUP_STORE_REGION"];
  return configured.every((key) => credential("integrations", key) !== null);
}
