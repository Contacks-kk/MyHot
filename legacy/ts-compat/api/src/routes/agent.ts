// Site modules can add Agent Markdown abilities. The core /api/v1/agent routes are served by Go.
import type { FastifyInstance, FastifyReply, FastifyRequest } from "fastify";
import { requestNotice, serverModules } from "@aihot/backend/modules";
import { sendTextWithEtag, strictQuery } from "../http/respond.ts";
import { publicHandler } from "./v1.ts";

function markdown(req: FastifyRequest, reply: FastifyReply, text: string, etagPrefix: string, cacheControl: string) {
  const note = requestNotice("agent", req);
  if (note) text += note;
  return sendTextWithEtag(req, reply, text, { etagPrefix, cacheControl, contentType: "text/markdown; charset=utf-8" });
}

export function registerAgent(app: FastifyInstance) {
  for (const ability of serverModules().flatMap((module) => module.agent?.abilities ?? [])) {
    app.get(`/api/v1/agent${ability.path}`, publicHandler(async (req, reply) => {
      strictQuery(req, []);
      return markdown(req, reply, await ability.answer(), ability.etagPrefix, ability.cacheControl);
    }));
  }
}
