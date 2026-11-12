import assert from "node:assert/strict";
import { test } from "node:test";
import { createCodeChecker, confirmRegistrationSession, type CodeCheckState } from "../app/features/account/code-check.ts";

const success = () => Response.json({verified:true,serverTime:"2026-10-14T00:00:00Z",expiresAt:"2026-10-14T00:01:00Z"});

test("six digits auto-check once, incomplete input and editing revoke success",async()=>{
  const states: CodeCheckState[]=[];
  let calls=0;
  const checker=createCodeChecker(s=>states.push(s),()=>assert.fail("unexpected error"),(async()=>{calls++;return success()}) as typeof fetch);
  await checker.update("reader@example.invalid","00123"); assert.equal(calls,0);
  await checker.update("reader@example.invalid","001234"); assert.equal(states.at(-1)?.status,"verified");
  await checker.update("reader@example.invalid","001234"); assert.equal(calls,1);
  await checker.update("reader@example.invalid","00123"); assert.equal(states.at(-1)?.status,"idle");
  checker.reset(); assert.equal(states.at(-1)?.status,"idle");
});

test("a stale success cannot replace a newer failure, and resend invalidates in-flight requests",async()=>{
  const states: CodeCheckState[]=[];
  const responses: Array<(response:Response)=>void>=[];
  const errors: Array<[string,boolean]>=[];
  const checker=createCodeChecker(s=>states.push(s),(message,invalid)=>errors.push([message,invalid]),(()=>new Promise(resolve=>responses.push(resolve))) as typeof fetch);
  const first=checker.update("reader@example.invalid","001234");
  const second=checker.update("other@example.invalid","654321");
  responses[1](Response.json({error:"验证码错误"},{status:400}));await second;
  responses[0](success());await first;
  assert.equal(states.at(-1)?.status,"failed"); assert.deepEqual(errors,[["验证码错误",true]]);
  const third=checker.update("other@example.invalid","654322");checker.reset();responses[2](success());await third;
  assert.equal(states.at(-1)?.status,"idle");
});

test("network failure is not reported as a wrong code",async()=>{
  let error: [string,boolean] | undefined;
  const checker=createCodeChecker(()=>{},(message,invalid)=>error=[message,invalid],(async()=>{throw new Error("网络异常")}) as typeof fetch);
  await checker.update("reader@example.invalid","001234");assert.deepEqual(error,["网络异常",false]);
});

test("registration confirms the cookie session before navigation; failure makes no second registration",async()=>{
  const paths:string[]=[];
  await confirmRegistrationSession((async(path,options)=>{paths.push(String(path));assert.equal(options?.credentials,"same-origin");return Response.json({id:1,csrfToken:"fixture-csrf"})}) as typeof fetch);
  assert.deepEqual(paths,["/api/me"]);
  await assert.rejects(confirmRegistrationSession((async()=>Response.json({error:"unauthorized"},{status:401})) as typeof fetch),/登录状态异常/);
});
