import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useLoaderData } from "react-router";
import type { LoaderFunctionArgs } from "react-router";
import { createCodeChecker, confirmRegistrationSession, type CodeCheckState } from "./code-check";
import { VerificationNotice } from "./VerificationNotice";
import { verificationDeadline, verificationSeconds } from "./verification-timing";
import { safeReturnTo } from "../../lib/access";
import { AccountFrame, fieldClass, FormMessage, labelClass, postJSON, submitClass } from "./common";

export async function loginLoader({ request }: LoaderFunctionArgs) {
  const url = new URL(request.url);
  const returnTo = safeReturnTo(url.searchParams.get("return"));
  return { returnTo };
}

export function LoginPage() {
  const { returnTo } = useLoaderData<typeof loginLoader>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setBusy(true); setError("");
    const fields = new FormData(event.currentTarget);
    try {
      await postJSON("/api/auth/login", { email: fields.get("email"), password: fields.get("password") });
      window.location.assign(returnTo);
    } catch (e) { setError(e instanceof Error ? e.message : "登录失败"); }
    finally { setBusy(false); }
  }
  return <AccountFrame title="登录 MyHOT" description="登录后查看为你整理的个人热点。">
    <form onSubmit={submit}>
      <label className={labelClass} htmlFor="email">邮箱</label>
      <input className={fieldClass} id="email" name="email" type="email" autoComplete="email" required />
      <label className={labelClass} htmlFor="password">密码</label>
      <input className={fieldClass} id="password" name="password" type="password" autoComplete="current-password" required />
      {error && <FormMessage error>{error}</FormMessage>}
      <button className={submitClass} disabled={busy}>{busy ? "正在登录…" : "登录"}</button>
    </form>
    <div className="mt-4 flex justify-between text-[12.5px] text-ink-3">
      <Link to={`/password-reset?${new URLSearchParams({ return: returnTo })}`} className="hover:text-accent">忘记密码</Link>
      <Link to="/register" className="hover:text-accent">创建账号</Link>
    </div>
    <p className="mt-3 text-center text-[12px] text-ink-4"><Link to="/verify-email" className="hover:text-accent">输入邮箱验证码</Link></p>
  </AccountFrame>;
}

export async function registerLoader({ request }: LoaderFunctionArgs) {
  const response = await fetch(`${process.env.GO_API_BASE_URL || "http://127.0.0.1:3002"}/api/auth/registration-status`, { signal: AbortSignal.any([request.signal, AbortSignal.timeout(3000)]), cache: "no-store" }).catch(() => null);
  return response?.ok ? await response.json() as { enabled: boolean } : { enabled: false };
}

function useVerificationCode(autoCheck = false) {
  const [email, setEmail] = useState("");
  const [code, setCode] = useState("");
  const [sending, setSending] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [retryDeadline, setRetryDeadline] = useState(0);
  const [expiryDeadline, setExpiryDeadline] = useState(0);
  const [now, setNow] = useState(0);
  const [validation, setValidation] = useState<CodeCheckState>({status:"idle"});
  const [notice, setNotice] = useState<"success" | "error" | null>(null);
  const checker = useRef<ReturnType<typeof createCodeChecker> | null>(null);
  if (!checker.current) checker.current = createCodeChecker(state => {
    setValidation(state);
    if (state.status === "verified") setNotice("success");
    else if (state.status === "idle") setNotice(null);
  }, (message, invalid) => {
    if (invalid) setNotice("error"); else setError(message);
  });
  useEffect(() => () => checker.current?.dispose(), []);
  useEffect(() => {
    if (validation.status !== "verified") return;
    const received = performance.now();
    setNow(received);
    setExpiryDeadline(verificationDeadline(validation.serverTime!, validation.expiresAt!, received));
  }, [validation]);
  function changeCode(value: string) {
    const digits = value.replace(/[^0-9]/g, "").slice(0,6);
    setCode(digits); setError("");
    if (autoCheck) void checker.current!.update(email,digits);
  }

  useEffect(() => {
    if (!retryDeadline && !expiryDeadline) return;
    const timer = window.setInterval(() => {
      const current = performance.now();
      setNow(current);
      if (current >= Math.max(retryDeadline, expiryDeadline)) window.clearInterval(timer);
    }, 250);
    return () => window.clearInterval(timer);
  }, [retryDeadline, expiryDeadline]);
  const remaining = (deadline: number) => verificationSeconds(deadline, now);
  function changeEmail(value: string) {
    checker.current!.reset();
    setEmail(value); setCode(""); setRetryDeadline(0); setExpiryDeadline(0); setError(""); setMessage("");
  }
  async function send() {
    checker.current!.reset();
    setSending(true); setError(""); setMessage("");
    try {
      const response = await fetch("/api/auth/verify/resend", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ email }) });
      const result = await response.json() as { error?: string; message?: string; serverTime?: string; expiresAt?: string; retryAt?: string };
      const received = performance.now();
      setNow(received);
      const deadline = (value: string) => verificationDeadline(result.serverTime!, value, received);
      if (result.serverTime && result.retryAt) setRetryDeadline(deadline(result.retryAt));
      if (result.serverTime && result.expiresAt) setExpiryDeadline(deadline(result.expiresAt));
      if (!response.ok) throw new Error(result.error || "验证码发送失败");
      setMessage(result.message || "验证码已发送"); setCode("");
    } catch (e) { setError(e instanceof Error ? e.message : "验证码发送失败"); }
    finally { setSending(false); }
  }
  return { email, changeEmail, code, changeCode, validation, notice, closeNotice: () => setNotice(null), sending, error, message, send, retrySeconds: remaining(retryDeadline), expirySeconds: remaining(expiryDeadline), expired: expiryDeadline > 0 && remaining(expiryDeadline) === 0 };
}

function VerificationFields({ state, busy }: { state: ReturnType<typeof useVerificationCode>; busy: boolean }) {
  return <>
    <VerificationNotice result={state.notice} onClose={state.closeNotice}/>
    <label className={labelClass} htmlFor="email">邮箱</label>
    <input className={fieldClass} id="email" name="email" type="email" autoComplete="email" required value={state.email} onChange={e => state.changeEmail(e.target.value)} disabled={busy || state.sending}/>
    <label className={labelClass} htmlFor="code">6位邮箱验证码</label>
    <div className="flex items-center gap-2">
      <input className={fieldClass} id="code" name="code" type="text" inputMode="numeric" autoComplete="one-time-code" pattern="[0-9]{6}" maxLength={6} required value={state.code} onChange={e => state.changeCode(e.target.value)} disabled={busy}/>
      <button type="button" className="mt-1.5 h-11 shrink-0 rounded-control border border-line-strong px-3 text-[12px] disabled:opacity-55" disabled={busy || state.sending || state.retrySeconds > 0 || !state.email} onClick={state.send}>{state.sending ? "发送中…" : state.retrySeconds > 0 ? `${state.retrySeconds}秒后重试` : "获取验证码"}</button>
    </div>
    {state.validation.status === "checking" && <p role="status" className="mt-2 text-[12px] text-ink-4">正在校验验证码…</p>}
    {state.validation.status === "verified" && !state.expired && <p role="status" className="mt-2 text-[12px] text-accent">验证码已验证</p>}
    {state.expirySeconds > 0 && <p role="status" className="mt-2 text-[12px] text-ink-4">验证码剩余 {state.expirySeconds} 秒有效</p>}
    {state.expired && <p role="status" className="mt-2 text-[12px] text-hot">验证码已过期，请重新获取。</p>}
    {state.error && <FormMessage error>{state.error}</FormMessage>}
    {state.message && <FormMessage>{state.message}</FormMessage>}
  </>;
}

export function RegisterPage() {
  const { enabled } = useLoaderData<typeof registerLoader>();
  const verification = useVerificationCode(true);
  const submitting = useRef(false);
  const [password, setPassword] = useState("");
  const [created, setCreated] = useState(false);
  const verified = verification.validation.status === "verified" && !verification.expired;
  const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (submitting.current || (!created && (!verified || password.length < 12))) return;
    submitting.current = true; setBusy(true); setError("");
    try {
      if (!created) {
        await postJSON("/api/auth/register", { email: verification.email, password, code: verification.code });
        setCreated(true);
      }
      await confirmRegistrationSession();
      window.location.assign("/");
    } catch (e) {
      setError(e instanceof Error ? e.message : "注册失败");
      submitting.current = false; setBusy(false);
    }
  }
  return <AccountFrame title="创建账号" description="填写邮箱验证码完成注册，验证码60秒内有效。">
    {!enabled ? <div className="mt-5 rounded-control border border-line bg-bg-sunk p-4 text-[13px] leading-relaxed text-ink-3">注册暂未开放，请稍后再试。</div> : <form onSubmit={submit}>
      <VerificationFields state={verification} busy={busy || created}/>
      <label className={labelClass} htmlFor="password">密码（至少 12 位）</label>
      <input className={fieldClass} id="password" name="password" type="password" minLength={12} autoComplete="new-password" required value={password} onChange={e => setPassword(e.target.value)} disabled={busy || created}/>
      {error && <FormMessage error>{error}</FormMessage>}
      <button className={submitClass} disabled={busy || verification.sending || (!created && (!verified || password.length < 12 || password.length > 256))}>{busy ? "正在登录…" : created ? "重新确认登录状态" : "注册并登录"}</button>
    </form>}
    <p className="mt-4 text-center text-[12.5px] text-ink-3">已有账号？ <Link to="/login" className="text-accent">登录</Link></p>
  </AccountFrame>;
}

export function VerifyEmailPage() {
  const verification = useVerificationCode();
  const [error,setError]=useState(""); const [message,setMessage]=useState(""); const [busy,setBusy]=useState(false);
  async function verify(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setBusy(true); setError("");
    try { const result = await postJSON<{message:string}>("/api/auth/verify", {email: verification.email, code: verification.code}); setMessage(result.message); }
    catch(e) { setError(e instanceof Error ? e.message : "验证失败"); }
    finally { setBusy(false); }
  }
  return <AccountFrame title="验证邮箱" description="获取6位邮箱验证码，60秒内填写并完成验证。">
    <form onSubmit={verify}><VerificationFields state={verification} busy={busy}/><button className={submitClass} disabled={busy || verification.sending || !!message}>{busy ? "正在验证…" : "完成邮箱验证"}</button></form>
    {error && <FormMessage error>{error}</FormMessage>}{message && <FormMessage>{message} <Link to="/login" className="text-accent">前往登录</Link></FormMessage>}
  </AccountFrame>;
}

export async function passwordLoader({ request }: LoaderFunctionArgs) { const url=new URL(request.url);return {token:url.searchParams.get("token")??""}; }

export function PasswordResetPage() {
  const {token}=useLoaderData<typeof passwordLoader>();const [busy,setBusy]=useState(false);const [message,setMessage]=useState("");const [error,setError]=useState("");
  async function submit(event:FormEvent<HTMLFormElement>){event.preventDefault();setBusy(true);setMessage("");setError("");const fields=new FormData(event.currentTarget);try{const result=token?await postJSON<{message:string}>("/api/auth/password/reset",{token,password:fields.get("password")}):await postJSON<{message:string}>("/api/auth/password/forgot",{email:fields.get("email")});setMessage(result.message);}catch(e){setError(e instanceof Error?e.message:"操作失败");}finally{setBusy(false);}}
  return <AccountFrame title={token?"设置新密码":"找回密码"} description={token?"新密码至少 12 位。":"输入账号邮箱，我们会发送密码重置链接。"}>
    <form onSubmit={submit}>{token?<><label className={labelClass} htmlFor="password">新密码</label><input className={fieldClass} id="password" name="password" type="password" minLength={12} autoComplete="new-password" required/></>:<><label className={labelClass} htmlFor="email">邮箱</label><input className={fieldClass} id="email" name="email" type="email" autoComplete="email" required/></>}
      {error&&<FormMessage error>{error}</FormMessage>}{message&&<FormMessage>{message}</FormMessage>}<button className={submitClass} disabled={busy}>{busy?"请稍候…":token?"更新密码":"发送重置邮件"}</button>
    </form>
    <p className="mt-4 text-center text-[12.5px] text-ink-3"><Link to="/login" className="text-accent">返回登录</Link></p>
  </AccountFrame>;
}
