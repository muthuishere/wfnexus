// conversation.mjs <start|status> — start a task on an external conversational
// service and read its status. Inputs arrive as in.* files written by the
// step (never interpolated into a command). The service URL and API key are
// read from the environment variables NAMED by in.service_url_env and
// in.api_key_env; the key is never printed.
//
//   start   POST {url}/tasks  (Idempotency-Key: <run id>:<step id>) → {task_id, status}
//   status  GET  {url}/tasks/{id} until terminal or the deadline     → {task_id, status, fields}
//
// Exit codes: 0 ok, 2 invalid input or configuration, 3 deadline reached,
// 4 the service refused or could not be reached.
import { readFileSync } from "node:fs";

const MAX_INPUT = 64 * 1024; // any one input
const MAX_TARGET = 4 * 1024; // one target, as JSON
const MAX_TARGETS = 200;
const MAX_FIELDS = 50;
const MAX_BODY = 1024 * 1024; // a service answer larger than this is refused
const MAX_VALUE = 16 * 1024; // a returned field value longer than this is cut
// Only variables meant for this module may be named, so a step cannot send
// another credential of the machine to the service.
const ENV_NAME = /^WFX_CONVERSATION_[A-Z0-9_]+$/;
const WORD = /^[a-z0-9._-]{1,64}$/;
const TERMINAL = new Set(["done", "failed", "cancelled"]);

function fail(code, msg) {
  process.stderr.write(scrub(msg) + "\n");
  process.exit(code);
}

let secretValue = "";
// scrub removes the key from anything printed, in case a service echoes it.
function scrub(s) {
  s = String(s);
  return secretValue && secretValue.length >= 4 ? s.split(secretValue).join("[redacted]") : s;
}

function input(name, { required = false, max = MAX_INPUT } = {}) {
  let v = "";
  try {
    v = readFileSync(`in.${name}`, "utf8");
  } catch {}
  if (v.endsWith("\n")) v = v.slice(0, -1);
  if (Buffer.byteLength(v) > max) fail(2, `${name} is longer than ${max} bytes`);
  if (required && v.trim() === "") fail(2, `${name} is required`);
  return v;
}

function jsonInput(name, check) {
  const raw = input(name, { required: true });
  let v;
  try {
    v = JSON.parse(raw);
  } catch {
    fail(2, `${name} must be JSON`);
  }
  check(v);
  return v;
}

function service() {
  const urlName = input("service_url_env", { required: true, max: 128 });
  const keyName = input("api_key_env", { required: true, max: 128 });
  for (const [what, n] of [["service_url_env", urlName], ["api_key_env", keyName]]) {
    if (!ENV_NAME.test(n)) fail(2, `${what} must name a variable starting with WFX_CONVERSATION_ (e.g. WFX_CONVERSATION_KEY)`);
  }
  const key = process.env[keyName] || "";
  if (!key) fail(2, `the environment variable ${keyName} is not set`);
  secretValue = key;
  const raw = process.env[urlName] || "";
  if (!raw) fail(2, `the environment variable ${urlName} is not set`);
  let url;
  try {
    url = new URL(raw);
  } catch {
    fail(2, `${urlName} is not a URL`);
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") fail(2, `${urlName} must be an http(s) URL`);
  const host = url.hostname.replace(/^\[|\]$/g, "");
  const loopback = host === "localhost" || host === "::1" || /^127\.\d+\.\d+\.\d+$/.test(host);
  if (url.protocol === "http:" && !loopback) fail(2, `${urlName} must be https (plain http only for localhost)`);
  url.search = "";
  url.hash = "";
  const base = url.toString().replace(/\/+$/, "");
  return { base, key, where: url.origin + url.pathname.replace(/\/+$/, "") };
}

async function call(svc, method, path, body, extra = {}) {
  let res;
  try {
    res = await fetch(svc.base + path, {
      method,
      headers: { Authorization: `Bearer ${svc.key}`, Accept: "application/json", ...(body ? { "Content-Type": "application/json" } : {}), ...extra },
      body: body ? JSON.stringify(body) : undefined,
      signal: AbortSignal.timeout(30000),
      // Never follow a redirect: the key must not go to another origin.
      redirect: "error",
    });
  } catch (e) {
    fail(4, `could not reach ${svc.where}${path.split("?")[0]} (redirects are refused): ${e.name}`);
  }
  const text = await readCapped(res);
  if (text === null) fail(4, `${method} ${path.split("?")[0]}: the answer is larger than ${MAX_BODY} bytes`);
  if (!res.ok) fail(4, `${method} ${svc.where}${path.split("?")[0]}: ${res.status} ${text.slice(0, 500)}`);
  try {
    return JSON.parse(text);
  } catch {
    fail(4, `${method} ${path.split("?")[0]}: the service did not answer JSON`);
  }
}

async function readCapped(res) {
  const chunks = [];
  let size = 0;
  for await (const c of res.body ?? []) {
    size += c.length;
    if (size > MAX_BODY) return null;
    chunks.push(Buffer.from(c));
  }
  return Buffer.concat(chunks).toString("utf8");
}

// capFields cuts any returned field value longer than MAX_VALUE (as JSON for
// a value that is not a string), marking the cut.
function capFields(fields) {
  if (fields === null || typeof fields !== "object" || Array.isArray(fields)) return {};
  const out = {};
  for (const [k, v] of Object.entries(fields)) {
    const s = typeof v === "string" ? v : JSON.stringify(v);
    out[k] = s.length > MAX_VALUE ? s.slice(0, MAX_VALUE) + "…[truncated]" : v;
  }
  return out;
}

const out = (o) => process.stdout.write(scrub(JSON.stringify(o)) + "\n");

async function start() {
  const type = input("task_type", { required: true, max: 4096 });
  const channel = input("channel", { required: true, max: 4096 });
  if (!WORD.test(type)) fail(2, "task_type must be 1 to 64 characters of a-z 0-9 . _ -");
  if (!WORD.test(channel)) fail(2, "channel must be 1 to 64 characters of a-z 0-9 . _ -");
  const targets = jsonInput("targets", (v) => {
    if (!Array.isArray(v) || v.length === 0) fail(2, "targets must be a non-empty JSON array");
    if (v.length > MAX_TARGETS) fail(2, `targets has more than ${MAX_TARGETS} entries`);
    for (const t of v) {
      if (typeof t !== "string" && (typeof t !== "object" || t === null || Array.isArray(t))) fail(2, "each target must be a string or an object");
      if (Buffer.byteLength(JSON.stringify(t)) > MAX_TARGET) fail(2, `a target is longer than ${MAX_TARGET} bytes`);
    }
  });
  const fields = jsonInput("fields", (v) => {
    if (!Array.isArray(v) || v.length === 0) fail(2, "fields must be a non-empty JSON array of names");
    if (v.length > MAX_FIELDS) fail(2, `fields has more than ${MAX_FIELDS} names`);
    for (const f of v) if (typeof f !== "string" || f === "" || f.length > 128) fail(2, "each field must be a name of 1 to 128 characters");
  });
  const run = process.env.WFX_RUN_ID || "";
  const step = process.env.WFX_STEP_ID || "";
  if (!run || !step) fail(2, "WFX_RUN_ID and WFX_STEP_ID are not set; the idempotency key needs them");
  const svc = service();
  const r = await call(svc, "POST", "/tasks", { type, targets, fields, channel }, { "Idempotency-Key": `${run}:${step}` });
  const id = r.task_id ?? r.id;
  if (!id) fail(4, "the service answered without a task id");
  out({ task_id: String(id), status: String(r.status ?? "accepted") });
}

async function status() {
  const id = input("task_id", { required: true, max: 256 });
  const num = (name, def, lo, hi) => {
    const raw = input(name).trim();
    if (raw === "") return def;
    const n = Number(raw);
    if (!Number.isFinite(n) || n < lo || n > hi) fail(2, `${name} must be a number from ${lo} to ${hi}`);
    return n;
  };
  const wait = num("wait_seconds", 0, 0, 3600);
  const poll = num("poll_seconds", 10, 0.1, 600);
  const svc = service();
  const deadline = Date.now() + wait * 1000;
  for (;;) {
    const r = await call(svc, "GET", `/tasks/${encodeURIComponent(id)}`);
    const st = String(r.status ?? "unknown");
    if (TERMINAL.has(st) || wait === 0) {
      out({ task_id: id, status: st, fields: capFields(r.fields ?? {}) });
      return;
    }
    if (Date.now() + poll * 1000 > deadline) fail(3, `deadline reached: last status ${st}`);
    await new Promise((ok) => setTimeout(ok, poll * 1000));
  }
}

const mode = process.argv[2];
if (mode === "start") await start();
else if (mode === "status") await status();
else fail(2, `unknown mode ${mode}`);
