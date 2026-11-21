// Real production SSR and proxy with synthetic local Go APIs. No external requests.
import assert from "node:assert/strict";
import { spawn, type ChildProcess } from "node:child_process";
import { once } from "node:events";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import { after, before, test } from "node:test";
import { fileURLToPath } from "node:url";
import * as cheerio from "cheerio";

let web: ChildProcess;
let origin: string;
let logs = "";
let failAccount = false;
let failCatalog = false;
let registrationEnabled = false;
const cookies: Array<{ path: string; cookie: string | undefined }> = [];
const adminApiPaths: string[] = [];
const goUserPaths: Array<{ path: string; cookie: string | undefined }> = [];
const forwarded: Array<{ host: string | string[] | undefined; ip: string | string[] | undefined }> = [];
const hotQueries: string[] = [];
const goApi = createServer((req,res)=>{
  const pathname = new URL(req.url!,"http://api.local").pathname;
  cookies.push({ path: pathname, cookie: req.headers.cookie });
  res.setHeader("Content-Type","application/json");
  if(pathname==="/api/site/meta") return res.end(JSON.stringify({changelogVersion:null}));
  if(pathname==="/api/auth/login") return res.end(JSON.stringify({owner:"fixture-admin"}));
  if(pathname==="/api/site/echo-client") return res.end(JSON.stringify({forwarded:req.headers["x-forwarded-for"],real:req.headers["x-real-ip"]}));
  if(pathname.startsWith("/api/admin/")){res.statusCode=401;return res.end(JSON.stringify({error:"unauthorized"}));}
  if(req.headers.cookie!=="myhot_session=alice"){res.statusCode=401;return res.end(JSON.stringify({error:"unauthorized"}));}
  if(pathname==="/api/site/reports/daily") return res.end(JSON.stringify({kind:"daily",items:[]}));
  if(pathname==="/api/site/stories/merged"){res.statusCode=308;return res.end(JSON.stringify({mergedInto:"survivor"}));}
  res.statusCode=404;res.end("{}");
});
const account = createServer((req,res)=>{
  const pathname=new URL(req.url!,"http://account.local").pathname;
  goUserPaths.push({path:pathname,cookie:req.headers.cookie});
  res.setHeader("Content-Type","application/json");
  if(pathname==="/api/auth/login"&&req.method==="GET") return res.end(JSON.stringify({owner:"go-admin-auth"}));
  if(pathname==="/api/site/meta") return res.end(JSON.stringify({changelogVersion:null}));
  if(pathname==="/api/site/changelog") return res.end(JSON.stringify({latestVersion:"fixture",releases:[]}));
  if(pathname==="/api/auth/registration-status") return res.end(JSON.stringify({enabled:registrationEnabled}));
  if(pathname==="/api/admin/runs") { adminApiPaths.push(pathname); return res.end(JSON.stringify({checkedAt:"2026-10-07T00:00:00Z",processes:[],jobs:[],timeline:[],queues:[],failedJobs:[],grouping:{waiting:0,needsAttention:0,items:[]},lagging:[],receipts:{counts:{},issues:[]},deliveries:[],errors:[],retrying:{count:0,next:null},ingest:[],modules:{}})); }
  if(pathname==="/api/admin/catalog") {
    adminApiPaths.push(pathname);
    if(!req.headers.cookie?.includes("aihot_admin=")) {
      res.statusCode=401;
      return res.end(JSON.stringify({error:"unauthorized"}));
    }
    if(failCatalog) {
      res.statusCode=503;
      return res.end("{}");
    }
    return res.end(JSON.stringify({subject:"Fixture",categories:[],topicGroups:[],topics:[]}));
  }
  if(pathname.startsWith("/api/admin/")){adminApiPaths.push(pathname);if(pathname==="/api/admin/me"&&!req.headers.cookie?.includes("aihot_admin=")){res.statusCode=401;return res.end(JSON.stringify({error:"unauthorized"}));}if(pathname==="/api/admin/me")return res.end(JSON.stringify({name:"Admin",csrf:"test-admin-csrf",dev:false}));if(pathname==="/api/admin/nav-counts")return res.end(JSON.stringify({feedback:1,sources:2,runs:3}));if(pathname==="/api/admin/sources")return res.end(JSON.stringify({page:1,rows:[],totals:{total:0,enabled:0,failing:0,degraded:0}}));if(pathname==="/api/admin/content")return res.end(JSON.stringify({rows:[]}));}
  if(pathname==="/api/auth/login"&&req.method==="POST"){forwarded.push({host:req.headers["x-forwarded-host"],ip:req.headers["x-forwarded-for"]});return res.end(JSON.stringify({owner:"go-users"}));}
  if(failAccount){res.statusCode=503;return res.end("{}");}
  const user = req.headers.cookie?.includes("myhot_session=alice")?"alice":req.headers.cookie?.includes("myhot_session=bob")?"bob":null;
  if(!user){res.statusCode=401;return res.end(JSON.stringify({error:"unauthorized"}));}
  if(pathname==="/api/me") return res.end(JSON.stringify({id:user==="alice"?1:2,email:`${user}@example.invalid`,csrfToken:"test-csrf"}));
  if(pathname==="/api/me/catalog") {
    if(failCatalog) {
      res.statusCode=503;
      return res.end("{}");
    }
    return res.end(JSON.stringify({subject:"Fixture",categories:[],topicGroups:[],topics:[]}));
  }
  if(pathname==="/api/site/reports/daily") return res.end(JSON.stringify({kind:"daily",items:[]}));
  if(pathname==="/api/me/model")return res.end(JSON.stringify({configured:true,baseUrl:"https://model.example.invalid/v1",model:"test",apiKeySet:true}));
  if(pathname==="/api/me/interests")return res.end(JSON.stringify({keywords:["AI"],entityIds:[],labelIds:[]}));
  if(pathname==="/api/me/labels")return res.end(JSON.stringify({labels:[{
    id:"agent", name:"Agent 智能体", kind:"topic", status:"active", aliases:["Agent","智能体"], definition:"自主规划与执行任务。", count:2, userCount:1,
  }]}));
  if(pathname==="/api/me/daily")return res.end(JSON.stringify({reports:[]}));
  if(pathname==="/api/me/feed/funnel")return res.end(JSON.stringify({totalScanned:3,passedCount:2,filteredCount:1,filteredItems:[]}));
  if(pathname==="/api/me/hot")hotQueries.push(req.url!);
  if(["/api/me/feed","/api/me/hot"].includes(pathname))return res.end(JSON.stringify({items:[{articleId:user,score:90,title:`${user} private title`,summary:`${user} private summary`,source:"Local",url:"https://example.invalid/article",createdAt:"2026-10-04T00:00:00Z"}]}));
  res.statusCode=404;res.end("{}");
});
before(async()=>{
  goApi.listen(0,"127.0.0.1");account.listen(0,"127.0.0.1");await Promise.all([once(goApi,"listening"),once(account,"listening")]);
  web=spawn(process.execPath,[fileURLToPath(new URL("../server.ts",import.meta.url))],{env:{...process.env,WEB_PORT:"0",TRUST_PROXY:"false",GO_API_BASE_URL:`http://127.0.0.1:${(account.address() as AddressInfo).port}`},stdio:["ignore","pipe","pipe"]});
  await new Promise<void>((resolve,reject)=>{const timeout=setTimeout(()=>reject(new Error(`web did not start: ${logs}`)),15000);web.on("exit",()=>{clearTimeout(timeout);reject(new Error(logs));});web.stderr!.on("data",chunk=>logs+=String(chunk));web.stdout!.on("data",chunk=>{logs+=String(chunk);const match=logs.match(/"msg":"web started","port":(\d+)/);if(match){origin=`http://127.0.0.1:${match[1]}`;clearTimeout(timeout);resolve();}});});
});
after(async()=>{if(web&&web.exitCode===null){web.kill("SIGTERM");await once(web,"exit");}goApi.closeAllConnections();account.closeAllConnections();await Promise.all([new Promise<void>(resolve=>goApi.close(()=>resolve())),new Promise<void>(resolve=>account.close(()=>resolve()))]);});
const alice={cookie:"myhot_session=alice"};
function privateResponse(response:Response){assert.equal(response.headers.get("Cache-Control"),"private, no-store");assert.equal(response.headers.get("X-Accel-Expires"),"0");}

test("anonymous landing and auth pages expose no private results and stay uncached",async()=>{
  for(const path of ["/","/_.data?_routes=root","/login","/register","/about","/terms","/privacy"]){const response=await fetch(origin+path);assert.equal(response.status,200,`${path}: ${logs}`);privateResponse(response);const body=await response.text();assert.doesNotMatch(body,/alice@example|bob@example|private summary/);}
});
test("open registration does not require policy checkboxes or version acceptance",async()=>{
  registrationEnabled=true;
  try {
    const response=await fetch(origin+"/register");
    assert.equal(response.status,200,logs);
    const body=await response.text();
    assert.match(body,/注册并登录/);
    assert.match(body,/name="code"/);
    assert.match(body,/获取验证码/);
    assert.match(body,/60秒内有效/);
    assert.doesNotMatch(body,/我已阅读并同意|确认版本：使用规则|name="terms"|name="privacy"/);
  } finally { registrationEnabled=false; }
});
test("personal and API-backed pages require a verified user session before rendering",async()=>{
  for(const path of ["/hot","/daily","/model","/interests","/history","/all","/items/example","/agent","/weekly","/monthly","/hot.data?_routes=root"]){const response=await fetch(origin+path,{redirect:"manual"});assert.equal(response.status,302,path);assert.match(response.headers.get("Location")!,/^\/login\?return=/);assert.equal(response.headers.get("Cache-Control"),"private, no-store");}
});
test("two user workspaces and their single-fetch identity are private and never mixed",async()=>{
  for(const user of ["alice","bob"]){for(const path of ["/","/hot.data?_routes=root","/hot"]){const response=await fetch(origin+path,{headers:{cookie:`myhot_session=${user}`}});assert.equal(response.status,200,logs);privateResponse(response);const body=await response.text();assert.match(body,new RegExp(user));assert.doesNotMatch(body,new RegExp(user==="alice"?"bob@example|bob private":"alice@example|alice private"));}}
  const loggedOut=await fetch(origin+"/");assert.doesNotMatch(await loggedOut.text(),/alice private|bob private/);
});

test("unified hotspots carry today's funnel, date controls, and the requested desktop and mobile navigation", async () => {
  const home = await fetch(origin + "/", { headers: alice, redirect: "manual" });
  assert.equal(home.status, 302);
  assert.equal(home.headers.get("Location"), "/hot");
  const response = await fetch(origin + "/hot?range=date&date=2000-01-02&page=2", { headers: alice });
  assert.equal(response.status, 200, logs);
  privateResponse(response);
  const $ = cheerio.load(await response.text());
  assert.equal($("h1").text(), "个人热点榜单");
  assert.match($("main").text(), /今日信息流与过滤动态/);
  assert.match($("main").text(), /2000-01-02 · 北京时间全天/);
  assert.deepEqual($("aside nav a").slice(0, 5).map((_, element) => $(element).text().trim()).get(), ["个人热点榜单", "关注项", "我的日报", "模型配置", "我的信源"]);
  assert.deepEqual($('nav[aria-label="底部导航"] a').map((_, element) => $(element).text().trim()).get(), ["个人热点榜单", "关注项", "我的日报", "我的账号"]);
  assert.deepEqual($('nav[aria-label="热点时间范围"]').children().map((_, element) => $(element).text().trim()).get(), ["今日", "48小时", "其他日期"]);
  assert.equal($('input[type="date"]').attr("value"), "2000-01-02");
  assert.ok(hotQueries.includes("/api/me/hot?range=date&page=2&date=2000-01-02"));
  assert.equal($("aside").text().includes("我的热点"), false);
  for (const range of ["today", "48h"]) {
    const selected = await fetch(origin + `/hot?range=${range}`, { headers: alice });
    assert.equal(selected.status, 200);
    await selected.text();
    assert.equal(hotQueries.at(-1), `/api/me/hot?range=${range}&page=1`);
  }
});
test("the verified workspace renders stable label choices with scoped recommendation counts", async () => {
  const response = await fetch(origin + "/interests", { headers: alice });
  assert.equal(response.status, 200, logs);
  privateResponse(response);
  const $ = cheerio.load(await response.text());
  assert.equal($("fieldset input[type=checkbox]").length, 1);
  assert.match($("fieldset").text(), /技术主题.*Agent 智能体.*2 篇/s);
  assert.match($("fieldset").text(), /1 位用户关注/);
  assert.ok(goUserPaths.some(row => row.path === "/api/me/labels" && row.cookie === alice.cookie));
});
test("report archive SSR forwards the user's own Cookie to Go and remains private",async()=>{
  const response=await fetch(origin+"/daily/archive",{headers:alice});assert.equal(response.status,200,logs);privateResponse(response);await response.text();assert.ok(goUserPaths.some(row=>row.path==="/api/site/reports/daily"&&row.cookie===alice.cookie));assert.ok(!cookies.some(row=>row.path==="/api/site/reports/daily"));
  const missing=await fetch(origin+"/items/missing",{headers:alice});assert.equal(missing.status,404);privateResponse(missing);await missing.text();
});
test("account outage returns 503 instead of redirecting a valid session to login",async()=>{
  failAccount=true;try{const response=await fetch(origin+"/hot",{headers:alice,redirect:"manual"});assert.equal(response.status,503);assert.equal(response.headers.get("Location"),null);assert.equal(response.headers.get("Cache-Control"),"private, no-store");await response.text();}finally{failAccount=false;}
});
test("catalog outages show a retryable error for users and administrators",async()=>{
  failCatalog=true;
  try {
    for(const [path,cookie] of [["/hot","myhot_session=alice"],["/admin/sources","aihot_admin=admin-session"]] as const) {
      const response=await fetch(origin+path,{headers:{cookie},redirect:"manual"});
      assert.equal(response.status,503,path);
      assert.match(await response.text(),/重新加载/);
    }
  } finally {
    failCatalog=false;
  }
});
test("user and admin login entrypoints go to Go, forwarded headers cannot be spoofed",async()=>{
  const response=await fetch(origin+"/api/auth/login",{method:"POST",headers:{"Content-Type":"application/json","X-Forwarded-Host":"evil.invalid","X-Forwarded-For":"6.6.6.6"},body:"{}"});assert.deepEqual(await response.json(),{owner:"go-users"});assert.deepEqual(forwarded.at(-1),{host:new URL(origin).host,ip:"127.0.0.1"});
  const admin=await fetch(origin+"/api/auth/login");assert.deepEqual(await admin.json(),{owner:"go-admin-auth"});
});
test("single-fetch errors and admin redirects remain private plain text",async()=>{
  for(const [path,status] of [["/items/missing.data",404],["/admin/sources.data?_routes=admin-layout",202]] as const){const response=await fetch(origin+path,{headers:alice});assert.equal(response.status,status,path);privateResponse(response);assert.equal(response.headers.get("Content-Type"),"text/plain; charset=utf-8");await response.text();}
});
test("admin SSR identity and navigation counts go directly to the Go API",async()=>{
  const response=await fetch(origin+"/admin/sources.data?_routes=admin-layout",{headers:{cookie:"aihot_admin=admin-session"}});
  assert.equal(response.status,200);
  assert.ok(adminApiPaths.includes("/api/admin/me"));
  assert.ok(adminApiPaths.includes("/api/admin/nav-counts"));
  assert.ok(adminApiPaths.includes("/api/admin/catalog"));
  assert.ok(!cookies.some(row=>row.path==="/api/admin/nav-counts"));
  privateResponse(response);
  await response.text();

  const sources=await fetch(origin+"/admin/sources",{headers:{cookie:"aihot_admin=admin-session"}});
  assert.equal(sources.status,200,logs);
  assert.ok(adminApiPaths.includes("/api/admin/sources"), `Go paths: ${adminApiPaths.join(",")}; unexpected paths: ${cookies.map(row=>row.path).join(",")}`);
  assert.ok(!cookies.some(row=>row.path==="/api/admin/sources"));
  privateResponse(sources);
  await sources.text();

  const content=await fetch(origin+"/admin/content?q=fixture",{headers:{cookie:"aihot_admin=admin-session"}});
  assert.equal(content.status,200,logs);
  assert.ok(adminApiPaths.includes("/api/admin/content"));
  assert.ok(!cookies.some(row=>row.path==="/api/admin/content"));
  privateResponse(content);
  await content.text();

  const runs=await fetch(origin+"/admin/runs",{headers:{cookie:"aihot_admin=admin-session"}});
  assert.equal(runs.status,200,logs);
  assert.ok(adminApiPaths.includes("/api/admin/runs"));
  assert.ok(!cookies.some(row=>row.path==="/api/admin/runs"));
  privateResponse(runs);
  await runs.text();
});
