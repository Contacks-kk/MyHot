import { spawn, spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { createServer } from "node:net";
import path from "node:path";
import { createInterface } from "node:readline";
import { setTimeout as delay } from "node:timers/promises";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const npmCli = process.env.npm_execpath;
if (!npmCli) throw new Error("Run this script with `npm run dev:local`.");

const children = [];
const shutdown = new AbortController();
const dryRun = process.env.DEV_LOCAL_DRY_RUN === "1";
const showStacks = process.env.DEV_LOCAL_STACKS === "1";
const apiPort = 3711;
const webPort = 3611;

/** Print all local services with the same timestamp, component and level columns. */
function writeLog(label, level, message) {
  const time = new Date().toLocaleTimeString("en-GB", { hour12: false });
  console.log(`${time}  ${label.padEnd(9)}  ${level.padEnd(5)}  ${message}`);
}

/** Format Go and Web structured logs while preserving plain CLI output. */
function forwardLog(label, line) {
  if (!line.trim()) {
    return;
  }
  const message = line.trimStart().replace(/^(?:\d{4}\/\d{2}\/\d{2} )?\d{2}:\d{2}:\d{2} /, "");
  let entry;
  if (message.startsWith("{")) {
    try {
      entry = JSON.parse(message);
    } catch {
      // Non-JSON output uses the same columns below.
    }
  }
  if (entry && typeof entry.msg === "string" && typeof entry.level === "string") {
    const { time, level, msg, err, ...fields } = entry;
    const error = err?.cause ?? err;
    const context = Object.entries(fields).map(([key, value]) => `${key}=${JSON.stringify(value)}`);
    if (typeof error?.message === "string") {
      context.push(`error=${JSON.stringify(error.message)}`);
    } else if (typeof err === "string") {
      context.push(`error=${JSON.stringify(err)}`);
    }
    writeLog(label, level.toUpperCase(), [msg, ...context].join(" "));
    if (showStacks && typeof error?.stack === "string") {
      for (const frame of error.stack.split("\n").slice(1)) {
        writeLog(label, "", frame);
      }
    }
    return;
  }
  const plain = /^(?:(DEBUG|INFO|WARN|ERROR) )?(.*)$/.exec(message);
  writeLog(label, plain?.[1] ?? "", plain?.[2] ?? message);
}

/** Stop the API, Worker and Web process trees started by this launcher. */
function stop(code = 0) {
  if (shutdown.signal.aborted) {
    return;
  }
  shutdown.abort();
  process.exitCode = code;
  for (const child of children) {
    if (child.exitCode !== null || child.signalCode !== null || !child.pid) {
      continue;
    }
    if (process.platform === "win32") {
      const result = spawnSync("taskkill.exe", ["/pid", String(child.pid), "/t", "/f"], {
        stdio: "ignore",
        windowsHide: true,
      });
      if (result.status !== 0) {
        child.kill();
      }
    } else {
      try {
        process.kill(-child.pid);
      } catch (error) {
        if (error.code !== "ESRCH") {
          writeLog("dev-local", "ERROR", `Could not stop PID ${child.pid}: ${error.message}`);
        }
      }
    }
  }
}

/** Start a service, capture its output and stop the stack if it exits. */
function start(label, command, args) {
  const child = spawn(command, args, {
    cwd: root,
    env: process.env,
    detached: process.platform !== "win32",
    stdio: ["ignore", "pipe", "pipe"],
  });
  children.push(child);
  for (const stream of [child.stdout, child.stderr]) {
    createInterface({ input: stream, crlfDelay: Infinity }).on("line", (line) => forwardLog(label, line));
  }
  child.on("error", (error) => {
    writeLog(label, "ERROR", error.message);
    stop(1);
  });
  child.on("exit", (code, signal) => {
    if (!shutdown.signal.aborted) {
      writeLog(label, "ERROR", `Exited (${signal ?? code ?? "unknown"}). Stopping the local stack.`);
      stop(code > 0 && code <= 255 ? code : 1);
    }
  });
}

/** Detect a stale listener before launching any service. */
async function checkPort(port) {
  const listener = createServer();
  await new Promise((resolve, reject) => {
    listener.once("error", reject);
    listener.listen(port, "127.0.0.1", resolve);
  }).catch((error) => {
    if (error.code === "EADDRINUSE") {
      throw new Error(`Port ${port} is already in use. Stop the previous run before starting again.`);
    }
    throw error;
  });
  await new Promise((resolve) => listener.close(resolve));
}

/** Wait for the API to serve a successful health response before starting dependents. */
async function waitForApi() {
  const deadline = AbortSignal.timeout(120_000);
  const signal = AbortSignal.any([shutdown.signal, deadline]);
  while (!signal.aborted) {
    try {
      const response = await fetch(`${process.env.GO_API_BASE_URL}/api/health`, {
        signal: AbortSignal.any([signal, AbortSignal.timeout(2_500)]),
      });
      await response.body?.cancel();
      if (response.ok) {
        return;
      }
    } catch {
      // The API can refuse connections during startup or while a probe times out.
    }
    await delay(500, undefined, { signal }).catch(() => {});
  }
  if (!shutdown.signal.aborted) {
    throw new Error("Go API did not pass /api/health within 120 seconds. Check the go-api logs above.");
  }
}

if (!existsSync(path.join(root, ".env.development"))) {
  throw new Error("Missing .env.development. Copy .env.development.example and set the test database credentials.");
}

// Check Redis independently of the development API, worker, and database.
if (process.argv.includes("--check-redis")) {
  const result = spawnSync("go", ["-C", "backend-go", "run", "./cmd/redis-check"], {
    cwd: root,
    stdio: "inherit",
  });
  if (result.error) {
    throw result.error;
  }
  process.exit(result.status ?? 1);
}

// Keep the local API listener and frontend proxy on the same development port.
process.env.GO_API_ADDR = `127.0.0.1:${apiPort}`;
process.env.GO_API_BASE_URL = `http://${process.env.GO_API_ADDR}`;
process.env.PUBLIC_BASE_URL = `http://localhost:${webPort}`;
process.env.SITE_URL = process.env.PUBLIC_BASE_URL;

console.log("Starting Go API, Go Worker and React Router using .env.development");
console.log("Web: http://localhost:3611  Go API: http://127.0.0.1:3711");

if (dryRun) {
  console.log("[dry-run] go -C backend-go run ./cmd/api");
  console.log("[dry-run] go -C backend-go run ./cmd/worker");
  console.log(`[dry-run] ${process.execPath} ${npmCli} run dev --workspace=@aihot/web`);
  process.exit(0);
}

process.on("SIGINT", () => stop(0));
process.on("SIGTERM", () => stop(0));

try {
  if (!process.env.REDIS_URL?.trim()) {
    throw new Error("REDIS_URL is required for user sessions. Set it in .env.development and run npm run redis:check.");
  }
  await checkPort(apiPort);
  await checkPort(webPort);
  if (!shutdown.signal.aborted) {
    writeLog("dev-local", "INFO", "Starting Go API and waiting for /api/health");
    start("go-api", "go", ["-C", "backend-go", "run", "./cmd/api"]);
    await waitForApi();
  }
  if (!shutdown.signal.aborted) {
    writeLog("dev-local", "INFO", "Go API is healthy. Starting Go Worker and Web.");
    start("go-worker", "go", ["-C", "backend-go", "run", "./cmd/worker"]);
    start("web", process.execPath, [npmCli, "run", "dev", "--workspace=@aihot/web"]);
  }
} catch (error) {
  writeLog("dev-local", "ERROR", error.message);
  stop(1);
}
