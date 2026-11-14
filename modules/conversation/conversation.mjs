// conversation.mjs <start|status> — start a task on an external conversational
// service and read its status. Inputs arrive as in.* files written by the
// step (never interpolated into a command). The service URL and API key are
// read from the environment variables NAMED by in.service_url_env and
// in.api_key_env; the key is never printed, in any form.
//
//   start   POST {url}/tasks  (Idempotency-Key: <run id>:<step id>) → {task_id, status}
//   status  GET  {url}/tasks/{id} until terminal or the deadline     → {task_id, status, fields, truncated}
//
// Exit codes: 0 ok, 2 invalid input or configuration, 3 deadline reached,
// 4 the service refused, could not be reached, or answered badly,
// 5 the task finished as failed or cancelled.
import { readFileSync } from "node:fs";

const MAX_INPUT = 64 * 1024; // any one input
const MAX_TARGETS = 50;
const MAX_FIELDS = 50;
const MAX_BODY = 1024 * 1024; // a service answer larger than this is refused
const MAX_VALUE = 16 * 1024; // a returned field value longer than this is cut
const MIN_KEY = 16; // a shorter key could not be told apart from ordinary text
// Only variables meant for this module may be named, so a step cannot send
// another credential of the machine to the service.
const ENV_NAME = /^WFX_CONVERSATION_[A-Z0-9_]+$/;
const WORD = /^[a-z0-9._-]{1,64}$/;
const PHONE = /^\+[1-9][0-9]{6,14}$/; // E.164
const IDENT = /^[A-Za-z0-9._:-]{1,128}$/;
const FIELD = /^[a-z][a-z0-9_]{0,63}$/;
const TERMINAL = new Set(["done", "failed", "cancelled"]);

let forms = []; // every printable form of the key, longest first

function fail(code, msg) {
  process.stderr.write(scrub(msg) + "\n");
  process.exit(code);
}

// Anything unexpected ends the step with a clean message, never a stack.
process.on("uncaughtException", (e) => fail(4, `the service call failed: ${e?.name ?? "error"}`));
process.on("unhandledRejection", (e) => fail(4, `the service call failed: ${e?.name ?? "error"}`));

// scrub removes the key from anything printed, in every form a service might
// echo it back in: raw, JSON-escaped, base64 (both alphabets), hex, URL-encoded.
function scrub(s) {
  s = String(s);
  for (const f of forms) s = s.split(f).join("[redacted]");
  return s;
}

function keyForms(key) {
  const b = Buffer.from(key, "utf8");
  const all = [
    key,
    JSON.stringify(key).slice(1, -1),
    b.toString("base64"),
    b.toString("base64").replace(/=+$/, ""),
    b.toString("base64url"),
    b.toString("hex"),
    b.toString("hex").toUpperCase(),
    encodeURIComponent(key),
  ];
  for (const f of [...all]) all.push(JSON.stringify(f).slice(1, -1));
  return [...new Set(all.filter(Boolean))].sort((a, b) => b.length - a.length);
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

function requestTimeoutMs() {
  const raw = process.env.WFX_CONVERSATION_REQUEST_TIMEOUT;
  const n = raw ? Number(raw) : 30;
  return Number.isFinite(n) && n >= 1 && n <= 120 ? n * 1000 : 30000;
}

function service() {
  const urlName = input("service_url_env", { required: true, max: 128 });
  const keyName = input("api_key_env", { required: true, max: 128 });
  for (const [what, n] of [["service_url_env", urlName], ["api_key_env", keyName]]) {
    if (!ENV_NAME.test(n)) fail(2, `${what} must name a variable starting with WFX_CONVERSATION_ (e.g. WFX_CONVERSATION_KEY)`);
  }
  const key = process.env[keyName] || "";
  if (!key) fail(2, `the environment variable ${keyName} is not set`);
  if (key.length < MIN_KEY) fail(2, `the key in ${keyName} is shorter than ${MIN_KEY} characters`);
  forms = keyForms(key);
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
  return { base, key, where: url.origin + url.pathname.replace(/\/+$/, ""), timeout: requestTimeoutMs() };
}

async function call(svc, method, path, body, extra = {}) {
  const where = `${method} ${svc.where}${path.split("?")[0]}`;
  // One deadline per request, covering the answer's body too: a server that
  // trickles its answer is cut off like one that never answers.
  const signal = AbortSignal.timeout(svc.timeout);
  let res;
  try {
    res = await fetch(svc.base + path, {
      method,
      headers: { Authorization: `Bearer ${svc.key}`, Accept: "application/json", ...(body ? { "Content-Type": "application/json" } : {}), ...extra },
      body: body ? JSON.stringify(body) : undefined,
      signal,
      // Never follow a redirect: the key must not go to another origin.
      redirect: "error",
    });
  } catch (e) {
    fail(4, `${where}: no answer (${e?.name === "TimeoutError" ? `timed out after ${svc.timeout / 1000}s` : "unreachable; redirects are refused"})`);
  }
  const declared = Number(res.headers.get("content-length"));
  if (Number.isFinite(declared) && declared > MAX_BODY) fail(4, `${where}: the answer declares ${declared} bytes, more than ${MAX_BODY}`);
  let text;
  try {
    text = await readCapped(res);
  } catch (e) {
    fail(4, `${where}: the answer did not arrive in time (${e?.name ?? "error"})`);
  }
  if (text === null) fail(4, `${where}: the answer is larger than ${MAX_BODY} bytes`);
  if (!res.ok) fail(4, `${where}: ${res.status} ${text.slice(0, 500)}`);
  try {
    return JSON.parse(text);
  } catch {
    fail(4, `${where}: the service did not answer JSON`);
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
// a value that is not a string), marking the cut. Collected fields are text
// from the people contacted: untrusted.
function capFields(fields) {
  const out = {};
  let truncated = false;
  if (fields === null || typeof fields !== "object" || Array.isArray(fields)) return { out, truncated };
  for (const [k, v] of Object.entries(fields)) {
    const s = typeof v === "string" ? v : JSON.stringify(v);
    if (s.length > MAX_VALUE) {
      out[k] = s.slice(0, MAX_VALUE) + "…[truncated]";
      truncated = true;
    } else {
      out[k] = v;
    }
  }
  return { out, truncated };
}

const out = (o) => process.stdout.write(scrub(JSON.stringify(o)) + "\n");

const printable = (s) => typeof s === "string" && s.length >= 1 && s.length <= 128 && !/[\u0000-\u001f\u007f]/.test(s);

function checkTarget(t) {
  if (typeof t === "string") {
    if (!PHONE.test(t) && !IDENT.test(t)) fail(2, "a target must be an E.164 phone (+15550100), an identifier of A-Z a-z 0-9 . _ : - (1-128), or an object");
    return;
  }
  if (typeof t !== "object" || t === null || Array.isArray(t)) fail(2, "a target must be a string or an object");
  const keys = Object.keys(t);
  if (keys.length === 0) fail(2, "a target object needs id, phone or name");
  for (const k of keys) {
    const v = t[k];
    if (k === "id" && typeof v === "string" && IDENT.test(v)) continue;
    if (k === "phone" && typeof v === "string" && PHONE.test(v)) continue;
    if (k === "name" && printable(v)) continue;
    fail(2, `a target object may hold only id (identifier), phone (E.164) and name (up to 128 printable characters); "${String(k).slice(0, 32)}" is not one of them or is not valid`);
  }
}

async function start() {
  const type = input("task_type", { required: true, max: 4096 });
  const channel = input("channel", { required: true, max: 4096 });
  if (!WORD.test(type)) fail(2, "task_type must be 1 to 64 characters of a-z 0-9 . _ -");
  if (!WORD.test(channel)) fail(2, "channel must be 1 to 64 characters of a-z 0-9 . _ -");
  const targets = jsonInput("targets", (v) => {
    if (!Array.isArray(v) || v.length === 0) fail(2, "targets must be a non-empty JSON array");
    if (v.length > MAX_TARGETS) fail(2, `targets has more than ${MAX_TARGETS} entries`);
    v.forEach(checkTarget);
  });
  const fields = jsonInput("fields", (v) => {
    if (!Array.isArray(v) || v.length === 0) fail(2, "fields must be a non-empty JSON array of names");
    if (v.length > MAX_FIELDS) fail(2, `fields has more than ${MAX_FIELDS} names`);
    for (const f of v) if (typeof f !== "string" || !FIELD.test(f)) fail(2, "each field name must match ^[a-z][a-z0-9_]{0,63}$");
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

// taskId is the task_id input: the id itself, or start_task's whole output
// ({"task_id": …}), so a workflow can pass the earlier step's stdout as is.
function taskId() {
  let id = input("task_id", { required: true, max: 4096 }).trim();
  if (id.startsWith("{")) {
    try {
      id = String(JSON.parse(id).task_id ?? "");
    } catch {
      fail(2, "task_id must be an id or start_task's JSON output");
    }
  }
  if (id === "" || Buffer.byteLength(id) > 256) fail(2, "task_id must be an id of at most 256 bytes");
  return id;
}

async function status() {
  const id = taskId();
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
  let last = "unknown";
  if (wait > 0) {
    // The poll loop has its own deadline too: a request still open when it
    // passes does not keep the step alive.
    setTimeout(() => fail(3, `deadline reached: last status ${last}`), wait * 1000 + svc.timeout).unref();
  }
  for (;;) {
    const r = await call(svc, "GET", `/tasks/${encodeURIComponent(id)}`);
    const st = String(r.status ?? "unknown");
    last = st;
    if (TERMINAL.has(st) || wait === 0) {
      const { out: fields, truncated } = capFields(r.fields ?? {});
      out({ task_id: id, status: st, fields, truncated });
      if (st === "failed" || st === "cancelled") process.exitCode = 5;
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
