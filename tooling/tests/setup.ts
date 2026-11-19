/** validateTestDatabaseURL accepts only local disposable PostgreSQL databases. */
export function validateTestDatabaseURL(raw: string): string {
	let parsed: URL;
	try {
		parsed = new URL(raw);
	} catch {
		throw new Error("Automated tests require a local disposable PostgreSQL database URL");
	}
	const host = parsed.hostname.toLowerCase().replace(/^\[|\]$/g, "");
	if (parsed.protocol !== "postgres:" && parsed.protocol !== "postgresql:") {
		throw new Error("Automated tests require a PostgreSQL database URL");
	}
	if (host !== "localhost" && host !== "127.0.0.1" && host !== "::1") {
		throw new Error("Automated tests may only connect to PostgreSQL on this machine");
	}
	const database = decodeURIComponent(parsed.pathname.slice(1));
	if (database.toLowerCase() === "aihot_v2_test") {
		throw new Error("Automated tests may not use the shared aihot_v2_test database");
	}
	if (!/_(test|ci)$/i.test(database)) {
		throw new Error("Automated tests require a disposable database named *_test or *_ci");
	}
	return database;
}

validateTestDatabaseURL(process.env.DATABASE_URL ?? "");
