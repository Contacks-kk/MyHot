// Existing archive-read tests use a real verified account and session. Only the test client adds
// its Cookie; production builds and live request hooks keep the same authentication boundary.
import './setup.ts';
import { createHash, randomBytes } from 'node:crypto';
import type { FastifyInstance, InjectOptions } from 'fastify';
import { sql } from '@aihot/backend/db';
import { buildApp as buildProductionApp } from '../api/src/app.ts';

export async function readerSession(options: { verified?: boolean; expired?: boolean } = {}) {
  const token = randomBytes(32).toString('base64url');
  const [user] = await sql<{ id: number }[]>`INSERT INTO user_accounts(email, password_hash, verified_at)
    VALUES (${`reader-${token}@example.test`}, 'test-fixture-not-a-password', ${options.verified === false ? null : new Date()}) RETURNING id`;
  const hash = createHash('sha256').update(token).digest('hex');
  await sql`INSERT INTO user_sessions(user_id, token_hash, csrf_token, expires_at)
    VALUES (${user!.id}, ${hash}, 'test-reader-csrf', now() + (${options.expired ? '-1 hour' : '1 day'}::interval))`;
  return {
    userId: user!.id,
    token,
    cookie: `myhot_session=${token}`,
    close: () => sql`DELETE FROM user_accounts WHERE id = ${user!.id}`,
  };
}

const cookies = new WeakMap<FastifyInstance, string>();
export function readerCookie(app: FastifyInstance): string {
  const cookie = cookies.get(app);
  if (!cookie) throw new Error('the app has no test reader session');
  return cookie;
}

export async function buildApp(): Promise<FastifyInstance> {
  const session = await readerSession();
  const app = await buildProductionApp();
  cookies.set(app, session.cookie);
  app.addHook('onClose', async () => { await session.close(); });
  const inject = app.inject.bind(app);
  app.inject = ((input: string | InjectOptions, callback?: unknown) => {
    const options: InjectOptions = typeof input === 'string' ? { url: input } : input;
    const headers: NonNullable<InjectOptions["headers"]> = { ...options.headers };
    const existing = String(headers.cookie ?? headers.Cookie ?? '');
    delete headers.Cookie;
    headers.cookie = /(?:^|;)\s*myhot_session=/.test(existing) ? existing : [existing, session.cookie].filter(Boolean).join('; ');
    return callback ? Reflect.apply(inject, app, [{ ...options, headers }, callback]) : inject({ ...options, headers });
  }) as FastifyInstance['inject'];
  return app;
}
