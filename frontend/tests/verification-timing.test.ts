import assert from "node:assert/strict";
import { test } from "node:test";
import { verificationDeadline, verificationSeconds } from "../app/features/account/verification-timing.ts";

test("verification starts with the server's 60 seconds after SMTP completes, regardless of local clock skew", () => {
  const serverTime = "2026-10-13T12:00:30Z";
  const expiry = "2026-10-13T12:01:30Z";
  const received = 25_000;
  const deadline = verificationDeadline(serverTime, expiry, received);
  assert.equal(verificationSeconds(deadline, received), 60);
  assert.equal(verificationSeconds(deadline, received + 59_000), 1);
  assert.equal(verificationSeconds(deadline, received + 60_000), 0);
  assert.equal(verificationSeconds(deadline, received + 120_000), 0);
});

test("cooldown errors retain the server's remaining wait instead of resetting to 60 seconds", () => {
  const received = 8_000;
  const deadline = verificationDeadline("2026-10-13T12:00:45Z", "2026-10-13T12:01:00Z", received);
  assert.equal(verificationSeconds(deadline, received), 15);
  assert.equal(verificationSeconds(deadline, received + 15_000), 0);
  assert.equal(verificationSeconds(verificationDeadline("2026-10-13T12:02:00Z", "2026-10-13T12:01:00Z", received), received), 0);
});
