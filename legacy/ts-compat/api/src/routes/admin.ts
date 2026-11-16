// /api/admin/* compatibility routes for the retired TypeScript test host.
import type { FastifyInstance, FastifyReply, FastifyRequest } from "fastify";
import { actorOf } from "@aihot/backend/admin/auth";
import { overrideFields, rerun, setVisibility } from "@aihot/backend/admin/content";
import { detachFromFact, mergeStories } from "@aihot/backend/events/corrections";
import { sendProblem } from "../http/respond.ts";
import { adminHandler } from "./admin-auth.ts";

export const body = <T = Record<string, unknown>>(req: FastifyRequest) => (req.body ?? {}) as T;
export const param = (req: FastifyRequest, name: string) => (req.params as Record<string, string>)[name]!;
const notFound = (req: FastifyRequest, reply: FastifyReply) => sendProblem(req, reply, { status: 404, code: "not_found", detail: "Not found." });
export const orNotFound = <T>(req: FastifyRequest, reply: FastifyReply, value: T | null) => (value === null || value === undefined ? notFound(req, reply) : value);
export function registerAdmin(app: FastifyInstance, goOwnsSharedContentCommands = false) {
  // Content and events
  if (!goOwnsSharedContentCommands) {
    app.post("/api/admin/content/:id/visibility", adminHandler(async (req, _reply, admin) => setVisibility(param(req, "id"), body(req) as never, actorOf(admin))));
    app.post("/api/admin/content/:id/override", adminHandler(async (req, _reply, admin) => overrideFields(param(req, "id"), body(req) as never, actorOf(admin))));
    app.post("/api/admin/content/:id/rerun", adminHandler(async (req, reply, admin) => {
      const b = body<{ step: "extract" | "analyze" | "group" }>(req);
      const requestId = String(req.headers["idempotency-key"] ?? "");
      return orNotFound(req, reply, await rerun(param(req, "id"), b.step, requestId, actorOf(admin)));
    }));
    app.post("/api/admin/content/:id/detach", adminHandler(async (req, _reply, admin) => detachFromFact(param(req, "id"), String(body(req).reason ?? ""), actorOf(admin))));
    app.post("/api/admin/stories/merge", adminHandler(async (req, _reply, admin) => {
      const b = body<{ from: number; into: number; reason: string }>(req);
      return mergeStories(Number(b.from), Number(b.into), b.reason, actorOf(admin));
    }));
  }

}
