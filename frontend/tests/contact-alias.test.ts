import assert from "node:assert/strict";
import { test } from "node:test";
import { contactAliasTarget } from "../app/lib/api-proxy.server.ts";

test("configured contact root aliases map to the Go contact service", () => {
  const aliases = [
    { slot: "wechat", file: "contact-wechat.png" },
    { slot: "feishu", file: "contact-feishu.png" },
  ] as const;
  assert.equal(contactAliasTarget("/contact-wechat.png", aliases), "/api/site/contact-alias/wechat");
  assert.equal(contactAliasTarget("/contact-feishu.png", aliases), "/api/site/contact-alias/feishu");
  assert.equal(contactAliasTarget("/other.png", aliases), null);
  assert.equal(contactAliasTarget("/contact-wechat.png?source=external", aliases), null);
});
