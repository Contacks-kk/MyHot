import assert from "node:assert/strict";
import { test } from "node:test";
import { isAnonymousPage, pagePath, safeReturnTo } from "../app/lib/access.ts";
test("single-fetch and origin-form slash paths follow the same authentication boundary",()=>{
  for(const path of ["/", "//", "/_.data", "/login.data", "/terms"]) assert.equal(isAnonymousPage(path),true,path);
  for(const path of ["/hot", "/hot.data", "//items/id", "/api/me", "/agent"]) assert.equal(isAnonymousPage(path),false,path);
  assert.equal(pagePath("//hot.data"),"/hot");
});
test("login return rejects paths browsers would normalize to another origin",()=>{
  for(const target of [null,"https://evil.invalid","//evil.invalid","/\\evil.invalid","/\n/evil.invalid", "\\evil.invalid"]) assert.equal(safeReturnTo(target),"/",String(target));
  assert.equal(safeReturnTo("/history?page=2"),"/history?page=2");
});
