// Read-only whole-site check: anonymous boundaries are always checked. Optionally read a local
// verified user's Cookie file to check personal SSR/API and public content endpoints as well.
// node tooling/scripts/smoke.ts --base http://localhost:3000 [--cookie-file path] [--registration-open]
import { readFileSync } from 'node:fs';
import { SITE } from '@aihot/site';

const option = (name: string) => { const at = process.argv.indexOf(name); return at < 0 ? undefined : process.argv[at + 1]; };
if (process.argv.includes('--help')) {
  console.log('node tooling/scripts/smoke.ts --base http://localhost:3000 [--cookie-file path] [--registration-open]');
  console.log('The Cookie file contains myhot_session=<value>. It is read locally and never printed.');
  process.exit(0);
}
const target = new URL(option('--base') ?? process.env.SITE_URL ?? 'http://localhost:3000');
if (!['http:', 'https:'].includes(target.protocol) || target.username || target.password || target.search || target.hash || target.pathname !== '/') throw new Error('--base must be an HTTP(S) origin');
const base = target.origin;
const cookiePath = option('--cookie-file');
const cookie = cookiePath ? readFileSync(cookiePath, 'utf8').trim() : '';
if (cookiePath && (!/(?:^|;)\s*myhot_session=[^;\s]+/.test(cookie) || /[\r\n]/.test(cookie))) throw new Error('the Cookie file must contain one line with myhot_session');
const registrationOpen = process.argv.includes('--registration-open');
const noStore = (res: Response) => /(?:^|,)\s*no-store\s*(?:,|$)/i.test(res.headers.get('cache-control') ?? '');
const htmlName = SITE.name.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#x27;');
let failed = 0;
let passed = 0;

async function check(label: string, path: string, expectedStatus: number, expect: (res: Response, body: string) => string | null = () => null, init: RequestInit = {}) {
  try {
    const res = await fetch(base + path, { ...init, redirect: 'manual', signal: AbortSignal.timeout(30_000) });
    const body = res.headers.get('content-type')?.startsWith('image/') ? '' : await res.text();
    const problem = res.status !== expectedStatus ? `HTTP ${res.status}, expected ${expectedStatus}` : expect(res, body);
    console.log(`${problem ? '✗' : '✓'} ${label} ${path}${problem ? `  ${problem}` : ''}`);
    problem ? failed += 1 : passed += 1;
  } catch {
    console.log(`✗ ${label} ${path}  request failed`);
    failed += 1;
  }
}

const namedPage = (res: Response, body: string) => !/text\/html/.test(res.headers.get('content-type') ?? '') ? 'expected HTML' : !body.includes(SITE.name) && !body.includes(htmlName) ? 'site identity missing' : !noStore(res) ? 'page must be no-store' : null;
for (const path of ['/', '/about', '/terms', '/privacy', '/login', '/register', '/verify-email', '/password-reset']) await check('anonymous', path, 200, namedPage);
await check('anonymous', '/admin/login', 200, (_res, body) => body.includes(SITE.name) || body.includes(htmlName) ? null : 'site identity missing');
await check('anonymous', '/api/health', 200, (res) => /json/.test(res.headers.get('content-type') ?? '') ? null : 'expected JSON');
await check('anonymous', '/api/site/meta', 200, (res) => /json/.test(res.headers.get('content-type') ?? '') ? null : 'expected JSON');
await check('anonymous', '/api/auth/registration-status', 200, (_res, body) => {
  try { return JSON.parse(body).enabled === registrationOpen ? null : `registration must be ${registrationOpen ? 'open' : 'closed'}`; } catch { return 'invalid registration status'; }
});
for (const [path, type] of [['/logo.svg', /svg/], ['/icon.png', /png/], ['/favicon.ico', /icon/], ['/manifest.webmanifest', /manifest/], ['/robots.txt', /text\/plain/]] as const) await check('brand', path, 200, (res) => type.test(res.headers.get('content-type') ?? '') ? null : 'unexpected content type');
for (const path of ['/hot', '/daily', '/model', '/interests', '/history', '/all', '/topics', '/agent']) await check('anonymous denied', path, 302, (res) => res.headers.get('location')?.startsWith('/login?') && noStore(res) ? null : 'expected uncached login redirect');
const privateEndpoints = ['/api/me', '/api/me/model', '/api/me/interests', '/api/me/entities', '/api/me/feed', '/api/me/hot', '/api/me/daily', '/api/me/history'];
const publicContentEndpoints: Array<[string, RegExp]> = [['/api/v1/items', /json/], ['/api/v1/hot-topics', /json/], ['/api/v1/selected/snapshot', /json/], ['/api/v1/agent', /text\/markdown/], ['/feed.xml', /xml/], ['/feed/all.xml', /xml/], ['/llms.txt', /text\/plain/], ['/sitemap.xml', /xml/], ['/openapi-v1.json', /json/], ['/og/site.png', /image\/png/]];
for (const path of [...privateEndpoints, ...publicContentEndpoints.map(([path]) => path)]) await check('anonymous denied', path, 401, (res) => noStore(res) ? null : 'authentication denial must be no-store');
const initialize: RequestInit = { method: 'POST', headers: { 'content-type': 'application/json', accept: 'application/json, text/event-stream' }, body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'myhot-smoke', version: '1' } } }) };
await check('anonymous denied', '/api/mcp', 401, (res) => noStore(res) ? null : 'MCP denial must be no-store', initialize);
if (cookie) {
  const headers = { cookie };
  await check('signed in', '/', 302, (res) => res.headers.get('location') === '/hot' && noStore(res) ? null : 'expected an uncached redirect to /hot', { headers });
  for (const path of ['/hot', '/daily', '/model', '/interests', '/history']) await check('signed in', path, 200, namedPage, { headers });
  for (const path of privateEndpoints) await check('signed in', path, 200, (res, body) => !noStore(res) ? 'personal API must be no-store' : !/json/.test(res.headers.get('content-type') ?? '') ? 'expected JSON' : /"apiKey"\s*:/.test(body) ? 'personal API must not return the API Key' : null, { headers });
  for (const [path, type] of publicContentEndpoints) await check('signed in', path, 200, (res) => !noStore(res) ? 'authenticated content must be no-store' : !type.test(res.headers.get('content-type') ?? '') ? 'unexpected content type' : null, { headers });
  await check('signed in', '/api/mcp', 200, (res, body) => !noStore(res) ? 'MCP must be no-store' : !body.includes(`"name":"${SITE.mcpPrefix}"`) ? 'MCP server identity missing' : null, { ...initialize, headers: { ...initialize.headers, cookie } });
}
console.log(`\n${passed} passed, ${failed} failed${cookie ? '' : '; authenticated checks skipped (no Cookie file)'}`);
process.exit(failed ? 1 : 0);
