// Production SSR with a local health stub. Failure cases: an access page describes daily reports
// but omits weekly/monthly support; a report RSS card offers a schedule without saying it carries
// that issue's contents; machine entry points exist but cannot be found from the access page.
import assert from 'node:assert/strict';
import { spawn, type ChildProcess } from 'node:child_process';
import { once } from 'node:events';
import { createServer } from 'node:http';
import type { AddressInfo } from 'node:net';
import { after, before, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import * as cheerio from 'cheerio';


let web: ChildProcess;
let origin: string;
let logs = '';
const api = createServer((req, res) => {
  res.setHeader('Content-Type', 'application/json');
  if (req.url === '/api/me') { if (req.headers.cookie !== 'myhot_session=reader') { res.statusCode = 401; return res.end('{}'); } return res.end(JSON.stringify({id:1,email:'reader@example.invalid',csrfToken:'csrf'})); }
  if (req.url === '/api/me/catalog') return res.end(JSON.stringify({subject:'Fixture',categories:[],topicGroups:[],topics:[]}));
  if (req.url === '/api/health') return res.end(JSON.stringify({ ok: true }));
  if (req.url === '/api/site/meta') return res.end(JSON.stringify({ changelogVersion: '2026-01-01T00:00' }));
  res.statusCode = 404;
  res.end(JSON.stringify({ code: 'not_found' }));
});
before(async () => {
  api.listen(0, '127.0.0.1');
  await once(api, 'listening');
  web = spawn(process.execPath, [fileURLToPath(new URL('../server.ts', import.meta.url))], {
    env: { ...process.env, WEB_PORT: '0', GO_API_BASE_URL: `http://127.0.0.1:${(api.address() as AddressInfo).port}` }, stdio: ['ignore', 'pipe', 'pipe'],
  });
  await new Promise<void>((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error(`web did not start: ${logs}`)), 15_000);
    web.on('exit', () => { clearTimeout(timeout); reject(new Error(`web exited: ${logs}`)); });
    web.stderr!.on('data', (chunk) => { logs += String(chunk); });
    web.stdout!.on('data', (chunk) => {
      logs += String(chunk);
      const match = logs.match(/"msg":"web started","port":(\d+)/);
      if (match) { origin = `http://127.0.0.1:${match[1]}`; clearTimeout(timeout); resolve(); }
    });
  });
});
after(async () => {
  if (web && web.exitCode === null) { web.kill('SIGTERM'); await once(web, 'exit'); }
  api.closeAllConnections();
  await new Promise<void>((resolve) => api.close(() => resolve()));
});

test('legacy machine access information requires login and offers no anonymous exports', async () => {
  const anonymous = await fetch(origin + '/agent', { redirect: 'manual' });
  assert.equal(anonymous.status, 302);
  const response = await fetch(origin + '/agent', { headers: { cookie: 'myhot_session=reader' } });
  assert.equal(response.status, 200, logs);
  assert.equal(response.headers.get('Cache-Control'), 'private, no-store');
  const $ = cheerio.load(await response.text());
  const visible = $('section').text();
  assert.match(visible, /个人结果不会进入原站 RSS/);
  assert.match(visible, /有效登录会话/);
  for (const path of ['/api/me/feed','/api/me/hot','/api/me/daily']) assert.ok(visible.includes(path));
  assert.doesNotMatch(visible, /匿名只读|不用 token|匿名 GET/);
});

test('anonymous introduction describes the personal daily schedule without new weekly or monthly features', async () => {
  const response = await fetch(origin + '/about');
  assert.equal(response.status, 200, logs);
  const $ = cheerio.load(await response.text());
  const visible = $('main').text();
  assert.match(visible, /北京时间08:00/);
  assert.match(visible, /此前24小时/);
  assert.match(visible, /关键词和公司实体/);
  assert.doesNotMatch(visible, /周报|月报|匿名/);
});
