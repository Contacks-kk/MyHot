import { spawn } from "node:child_process";
import { existsSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const npmCli = process.env.npm_execpath;
if (!npmCli) throw new Error("Run this script with `npm run dev:local`.");

const children = [];
let stopping = false;
let exitCode = 0;
const dryRun = process.env.DEV_LOCAL_DRY_RUN === "1";

function finishIfStopped() {
  if (stopping && children.every((child) => child.exited)) process.exitCode = exitCode;
}

function stop(code = 0) {
  if (stopping) return;
  stopping = true;
  exitCode = code;
  for (const child of children) {
    if (!child.exited) child.process.kill();
  }
  finishIfStopped();
}

function start(label, command, args) {
  const child = { label, exited: false, process: spawn(command, args, { cwd: root, env: process.env, stdio: "inherit" }) };
  children.push(child);
  child.process.on("error", (error) => {
    child.exited = true;
    console.error(`[${label}] ${error.message}`);
    stop(1);
  });
  child.process.on("exit", (code, signal) => {
    child.exited = true;
    if (!stopping) {
      console.error(`[${label}] exited (${signal ?? code ?? "unknown"}); stopping the local stack`);
      stop(code ?? 1);
    }
    finishIfStopped();
  });
}

if (!existsSync(path.join(root, ".env.development"))) {
  throw new Error("Missing .env.development. Copy .env.development.example and set the test database credentials.");
}

// Keep the local API listener and frontend proxy on the same development port.
process.env.GO_API_ADDR = "127.0.0.1:3711";
process.env.GO_API_BASE_URL = "http://127.0.0.1:3711";
process.env.PUBLIC_BASE_URL = "http://localhost:3611";
process.env.SITE_URL = "http://localhost:3611";

console.log("Starting Go API, Go Worker and React Router using .env.development");
console.log("Web: http://localhost:3611  Go API: http://127.0.0.1:3711");

if (dryRun) {
  console.log("[dry-run] go -C backend-go run ./cmd/api");
  console.log("[dry-run] go -C backend-go run ./cmd/worker");
  console.log(`[dry-run] ${process.execPath} ${npmCli} run dev --workspace=@aihot/web`);
  process.exit(0);
}

start("go-api", "go", ["-C", "backend-go", "run", "./cmd/api"]);
start("go-worker", "go", ["-C", "backend-go", "run", "./cmd/worker"]);
start("web", process.execPath, [npmCli, "run", "dev", "--workspace=@aihot/web"]);

process.on("SIGINT", () => stop(0));
process.on("SIGTERM", () => stop(0));
