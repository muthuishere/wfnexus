// browser.mjs — run the configured browser command with one profile.
//
//   node browser.mjs read|act   reads in.profile, in.action, in.args (files the
//                               step wrote; never interpolated into a command)
//
// read runs only the actions the profile lists in read_actions (none listed =
// no reads); act runs any action (the step is a write that pauses first).
//
// Config: $WFX_BROWSER_CONFIG or ~/.config/wfx/browser.json —
//   {"default":"us","profiles":{"us":{"command":["some-browser-cli"],
//    "folder":"/data/profiles/us","proxy":"http://127.0.0.1:8881",
//    "read_actions":["open","text"]}}}
//
// A profile that is named but not configured is refused (exit 2): the default
// is used only when no profile is named. The command runs with no shell, as
// argv + [action, ...args], with WFX_BROWSER_PROFILE, WFX_BROWSER_PROFILE_DIR,
// WFX_BROWSER_PROXY (and HTTP_PROXY/HTTPS_PROXY when a proxy is set). Its
// output passes through; the step exits with its code.
import { readFileSync, existsSync } from "node:fs";
import { homedir } from "node:os";
import { join, delimiter, isAbsolute } from "node:path";
import { spawn } from "node:child_process";

function fail(msg) {
  process.stderr.write("browser: " + msg + "\n");
  process.exit(2);
}

function input(name) {
  try {
    return readFileSync("in." + name, "utf8").replace(/\r?\n$/, "");
  } catch {
    return "";
  }
}

function onPath(cmd) {
  if (cmd.includes("/") || isAbsolute(cmd)) return existsSync(cmd);
  const exts = process.platform === "win32" ? ["", ".exe", ".cmd", ".bat"] : [""];
  for (const dir of (process.env.PATH || "").split(delimiter)) {
    for (const ext of exts) if (dir && existsSync(join(dir, cmd + ext))) return true;
  }
  return false;
}

const mode = process.argv[2];
if (mode !== "read" && mode !== "act") fail("usage: browser.mjs read|act");

// A proxy with user or user:pass@ is refused: credentials do not belong here.
function hasUserinfo(proxy) {
  if (!proxy) return false;
  let host = String(proxy);
  const i = host.indexOf("://");
  if (i >= 0) host = host.slice(i + 3);
  host = host.split(/[/?#]/)[0];
  return host.includes("@");
}

const path = process.env.WFX_BROWSER_CONFIG || join(homedir(), ".config", "wfx", "browser.json");
let cfg;
try {
  cfg = JSON.parse(readFileSync(path, "utf8"));
} catch (e) {
  fail(e.code === "ENOENT" ? `no browser profiles configured (${path})` : `${path}: ${e.message}`);
}
const profiles = cfg.profiles || {};
const names = Object.keys(profiles).sort();
if (names.length === 0) fail(`no browser profiles configured (${path})`);

const asked = input("profile").trim();
const name = asked || cfg.default || "";
if (!name) fail(`no profile named and no default; configured: ${names.join(", ")}`);
if (!Object.hasOwn(profiles, name)) {
  fail(`profile "${name}" is not configured; configured: ${names.join(", ")}`);
}
const p = profiles[name];
const argv = typeof p.command === "string" ? p.command.split(/\s+/).filter(Boolean) : p.command || [];
if (argv.length === 0) fail(`profile "${name}" has no command`);
if (hasUserinfo(p.proxy)) fail(`profile "${name}": proxy carries credentials; use a local proxy without them`);
if (!onPath(argv[0])) fail(`profile "${name}": command ${argv[0]} not found`);

const action = input("action").trim();
if (!action) fail("action is required");
if (mode === "read") {
  const allowed = Array.isArray(p.read_actions) ? p.read_actions.map(String) : [];
  if (!allowed.includes(action)) {
    fail(`read: action "${action}" is not in profile "${name}" read_actions (${allowed.join(", ") || "none"}); use browser.act for anything else`);
  }
}
let args = [];
const rawArgs = input("args");
if (rawArgs.trim() !== "") {
  try {
    const parsed = JSON.parse(rawArgs);
    args = Array.isArray(parsed) ? parsed.map(String) : [rawArgs];
  } catch {
    args = [rawArgs];
  }
}

const env = { ...process.env, WFX_BROWSER_PROFILE: name, WFX_BROWSER_PROFILE_DIR: p.folder || "", WFX_BROWSER_PROXY: p.proxy || "" };
if (p.proxy) {
  env.HTTP_PROXY = env.HTTPS_PROXY = env.http_proxy = env.https_proxy = p.proxy;
}
const child = spawn(argv[0], [...argv.slice(1), action, ...args], { env, stdio: "inherit", shell: false });
child.on("error", (e) => fail(`${argv[0]}: ${e.message}`));
child.on("exit", (code, signal) => process.exit(code ?? (signal ? 1 : 0)));
