export type CodeCheckState = { status: "idle" | "checking" | "verified" | "failed"; expiresAt?: string; serverTime?: string };

export function createCodeChecker(
  publish: (state: CodeCheckState) => void,
  report: (message: string, invalid: boolean) => void,
  request: typeof fetch = fetch,
) {
  let generation = 0;
  let lastInput = "";
  let pending: AbortController | undefined;
  function reset() {
    generation++; lastInput = ""; pending?.abort(); publish({ status: "idle" });
  }
  async function update(email: string, code: string) {
    const input = JSON.stringify([email.trim().toLowerCase(), code]);
    if (input === lastInput) return;
    reset(); lastInput = input;
    if (!email || !/^[0-9]{6}$/.test(code)) return;
    const current = generation;
    pending = new AbortController();
    publish({ status: "checking" });
    try {
      const response = await request("/api/auth/register/code/check", {
        method: "POST", credentials: "same-origin", cache: "no-store", signal: pending.signal,
        headers: { "Content-Type": "application/json" }, body: JSON.stringify({ email, code }),
      });
      const result = await response.json() as { verified?: boolean; serverTime?: string; expiresAt?: string; error?: string };
      if (current !== generation) return;
      if (!response.ok) {
        publish({ status: "failed" });
        report(response.status === 400 ? "验证码错误" : result.error || "验证码暂时无法校验", response.status === 400);
        return;
      }
      if (!result.verified || !result.serverTime || !result.expiresAt || !Number.isFinite(Date.parse(result.expiresAt))) throw new Error("验证码校验响应异常");
      publish({ status: "verified", serverTime: result.serverTime, expiresAt: result.expiresAt });
    } catch (error) {
      if (current !== generation) return;
      publish({ status: "failed" });
      report(error instanceof Error ? error.message : "验证码暂时无法校验", false);
    }
  }
  return { update, reset, dispose: () => { generation++; pending?.abort(); } };
}

export async function confirmRegistrationSession(request: typeof fetch = fetch): Promise<void> {
  const response = await request("/api/me", { credentials: "same-origin", cache: "no-store" });
  if (!response.ok) throw new Error("账号已创建，但登录状态异常，请留在当前页面，稍后重试登录状态确认。");
  const user = await response.json() as { id?: number; csrfToken?: string };
  if (!user.id || !user.csrfToken) throw new Error("登录状态确认失败，请稍后重试。");
}
