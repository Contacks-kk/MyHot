// Anchor server durations to the browser's monotonic clock, independent of clock skew and SMTP latency.
export function verificationDeadline(serverTime: string, deadline: string, receivedAt: number): number {
  return receivedAt + Math.max(0, Date.parse(deadline) - Date.parse(serverTime));
}

export function verificationSeconds(deadline: number, now: number): number {
  return Math.max(0, Math.ceil((deadline - now) / 1000));
}
