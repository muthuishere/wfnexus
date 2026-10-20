#!/usr/bin/env node
// apl-whatsapp — the reference wfnexus channel adapter (wfnexus.channel/v1).
//
// wfnexus runs this as a channel's `run:` and appends a verb:
//
//   node adapters/apl-whatsapp.js send     stdin: envelope   stdout: {"id": "..."}
//   node adapters/apl-whatsapp.js receive  stdin: {"after"}  stdout: {"messages": [...], "cursor": "..."}
//
// The contract is docs/channels.md; the JSON Schemas are docs/schemas/. This
// file is ONE implementation of it — WhatsApp groups through the apl CLI — and
// nothing in wfnexus knows it exists. Email, Slack or anything else is another
// file like this one.
//
// HOW IT REACHES WHATSAPP: through `apl with whatsapp:<label> -- wacli …`.
// apl maps the label to a wacli account; wacli does the protocol. Plain Node,
// no dependencies, Node 18+.
//
// SINGLE WRITER. One WhatsApp number must have one writer. On a machine where a
// live `wacli sync --follow` (apl serve) owns the account, `wacli send` does not
// open the store itself: it finds the syncer's send socket and DELEGATES the
// send to it, which also paces sends. Run this adapter on the machine that owns
// the account. It never runs `wacli sync`, and it never retries a send.
//
// Environment (set them in the channel's `env:` — values may be ${VAR}):
//
//   APL_WA_GROUP      the group JID (…@g.us). The envelope's `target` overrides it.
//   APL_WA_LABEL      the apl handle label (whatsapp:<label>); default "biz"
//   APL_BIN           the apl binary; default "apl"
//   APL_WA_TIMEOUT_MS the most one invocation may take; default 120000, and
//                     never longer than wfnexus allows (WFX_CHANNEL_TIMEOUT_MS,
//                     which wfnexus sets) less 5s — so the adapter gives up and
//                     SAYS so before it is killed
//   APL_WA_LIMIT      most messages one receive returns; default 200
//   APL_WA_RESET_WINDOW  messages re-read when the cursor is lost; default 20

"use strict";

const { spawnSync } = require("node:child_process");
const dns = require("node:dns").promises;
const net = require("node:net");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const VERSION = "wfnexus.channel/v1";
const env = process.env;
const label = env.APL_WA_LABEL || "biz";
const apl = env.APL_BIN || "apl";
const limit = Number(env.APL_WA_LIMIT || 200);
const resetWindow = Number(env.APL_WA_RESET_WINDOW || 20);

// One budget for the whole invocation, inside wfnexus's own kill deadline.
let budget = Number(env.APL_WA_TIMEOUT_MS || 120000);
if (env.WFX_CHANNEL_TIMEOUT_MS) budget = Math.min(budget, Math.max(1000, Number(env.WFX_CHANNEL_TIMEOUT_MS) - 5000));
const deadline = Date.now() + budget;
const remaining = () => Math.max(1000, deadline - Date.now());

function fail(msg) {
  process.stderr.write("apl-whatsapp: " + msg + "\n");
  process.exit(1);
}

// wacli runs through apl, with --json; its answer is {"success","data","error"}.
function wacli(args, { readOnly } = {}) {
  const argv = ["with", "whatsapp:" + label];
  if (readOnly) argv.push("--read-only");
  argv.push("--", "wacli", "--json", ...args);
  const timeout = remaining();
  const r = spawnSync(apl, argv, { encoding: "utf8", timeout, maxBuffer: 64 << 20 });
  if (r.error) {
    if (r.error.code === "ETIMEDOUT") throw new Error(`apl timed out after ${timeout}ms`);
    throw new Error(`could not run ${apl}: ${r.error.message}`);
  }
  let body = null;
  try {
    body = JSON.parse(r.stdout || "");
  } catch (_) {
    /* fall through to the exit status */
  }
  if (body && body.success) return body.data;
  const why = (body && body.error) || (r.stderr || "").trim() || `exit ${r.status}`;
  throw new Error(why);
}

function readStdin() {
  const raw = fs.readFileSync(0, "utf8").trim();
  if (!raw) return {};
  try {
    return JSON.parse(raw);
  } catch (e) {
    fail("stdin is not JSON: " + e.message);
  }
}

function groupOf(req) {
  const g = req.target || env.APL_WA_GROUP || "";
  if (!g.endsWith("@g.us")) fail("no group: set APL_WA_GROUP (or the envelope's target) to a …@g.us JID");
  return g;
}

// internal reports an address that is not on the public internet — the same
// rule wfnexus applies (notify.Internal) before the envelope reaches this
// script. IPv6 is parsed to its 8 hextets first, because Node's URL
// normalises https://[::ffff:127.0.0.1] to [::ffff:7f00:1]; any IPv4 an IPv6
// address CARRIES (mapped, compatible, NAT64, 6to4) is unwrapped and checked.
function internalV4(a, b) {
  return a === 0 || a === 10 || a === 127 || (a === 100 && b >= 64 && b < 128) ||
    (a === 169 && b === 254) || (a === 172 && b >= 16 && b < 32) || (a === 192 && b === 168) || a >= 224;
}

function hextets(ip) {
  let v = ip.toLowerCase().replace(/^\[|\]$/g, "").replace(/%.*$/, "");
  const dotted = v.match(/^(.*:)(\d+\.\d+\.\d+\.\d+)$/);
  if (dotted) {
    const q = dotted[2].split(".").map(Number);
    v = dotted[1] + ((q[0] << 8) | q[1]).toString(16) + ":" + ((q[2] << 8) | q[3]).toString(16);
  }
  const [head, tail] = v.includes("::") ? v.split("::") : [v, null];
  const h = head ? head.split(":") : [];
  const t = tail ? tail.split(":") : [];
  const fill = tail === null ? [] : new Array(8 - h.length - t.length).fill("0");
  return [...h, ...fill, ...t].map((x) => parseInt(x || "0", 16));
}

function internal(ip) {
  if (net.isIPv4(ip)) {
    const [a, b] = ip.split(".").map(Number);
    return internalV4(a, b);
  }
  const x = hextets(ip);
  if (x.length !== 8 || x.some((n) => Number.isNaN(n))) return true; // unparseable: refuse
  const zero = (from, to) => x.slice(from, to).every((n) => n === 0);
  const v4 = (hi) => internalV4(hi >> 8, hi & 0xff); // a.b from the high hextet decides every range here
  if (zero(0, 8)) return true; // ::
  if (zero(0, 7) && x[7] === 1) return true; // ::1
  if (zero(0, 5) && x[5] === 0xffff) return v4(x[6], x[7]); // ::ffff:a.b.c.d (mapped)
  if (zero(0, 6)) return v4(x[6], x[7]); // ::a.b.c.d (compatible)
  if (x[0] === 0x64 && x[1] === 0xff9b) return zero(2, 6) ? v4(x[6], x[7]) : true; // NAT64; 64:ff9b:1::/48 is local-use
  if (x[0] === 0x2002) return v4(x[1], x[2]); // 6to4
  if (x[0] === 0x2001 && x[1] === 0) return true; // Teredo: the IPv4 is obfuscated; refuse
  return (x[0] & 0xfe00) === 0xfc00 || (x[0] & 0xffc0) === 0xfe80 || (x[0] & 0xff00) === 0xff00;
}

// checkURL refuses anything but https to a public address. The server checked
// too; this re-check at fetch time narrows a DNS-rebinding window.
async function checkURL(raw) {
  const u = new URL(raw);
  if (u.protocol !== "https:") throw new Error(`attachment url must be https: ${raw}`);
  const host = u.hostname.replace(/^\[|\]$/g, "");
  const addrs = net.isIP(host) ? [{ address: host }] : await dns.lookup(host, { all: true });
  for (const a of addrs) {
    if (internal(a.address)) throw new Error(`attachment url ${u.hostname} resolves to an internal address (${a.address}); refused`);
  }
}

// fetchToFile downloads a URL attachment, because wacli sends files, not URLs.
// Redirects are refused: a public URL must not bounce the fetch inward.
async function fetchToFile(url, filename) {
  await checkURL(url);
  const res = await fetch(url, { redirect: "error", signal: AbortSignal.timeout(remaining()) });
  if (!res.ok) throw new Error(`attachment ${url}: HTTP ${res.status}`);
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "wfx-wa-"));
  const name = path.basename(filename || new URL(url).pathname) || "attachment";
  const file = path.join(dir, name);
  fs.writeFileSync(file, Buffer.from(await res.arrayBuffer()));
  return { file, cleanup: () => fs.rmSync(dir, { recursive: true, force: true }) };
}

async function send() {
  const e = readStdin();
  const group = groupOf(e);
  const text = e.text || "";
  let data;
  let cleanup = () => {};
  try {
    const a = e.attachment;
    if (a && (a.path || a.url)) {
      let file = a.path;
      if (!file) ({ file, cleanup } = await fetchToFile(a.url, a.filename));
      const args = ["send", "file", "--to", group, "--file", file];
      if (a.filename) args.push("--filename", a.filename);
      if (text) args.push("--caption", text);
      data = wacli(args);
    } else {
      if (!text) fail("nothing to send: the envelope has no text and no attachment");
      data = wacli(["send", "text", "--to", group, "--message", text]);
    }
  } catch (err) {
    cleanup();
    // NEVER retried, here or by wfnexus: a send that errored or timed out may
    // still have gone out, and a duplicate in a group is worse than an error.
    fail(`send failed: ${err.message}. The message MAY have landed — read the group before sending it again; do not retry blind.`);
  }
  cleanup();
  if (!data || data.sent === false) fail("send not confirmed by wacli. The message MAY have landed — read the group first; do not retry blind.");
  process.stdout.write(JSON.stringify({ id: String(data.id || "") }) + "\n");
}

// toMessage maps a wacli message (its store.Message fields: MsgID, SenderJID,
// SenderName, Timestamp, Text, DisplayText, MediaType, MediaCaption, Filename,
// MimeType) onto the contract's inbound message.
function toMessage(m) {
  const out = {
    id: m.MsgID,
    from: m.SenderJID || "",
    name: m.SenderName || "",
    text: m.Text || m.MediaCaption || m.DisplayText || "",
    at: m.Timestamp,
  };
  if (m.MediaType) {
    out.attachments = [{ filename: m.Filename || "", mime: m.MimeType || "" }];
  }
  return out;
}

// notFound is wacli's "that message is not in the store" — sql.ErrNoRows.
function notFound(err) {
  return /no rows in result set/i.test(String(err && err.message));
}

function receive() {
  const req = readStdin();
  const group = groupOf(req);
  try {
    if (!req.after) {
      // First poll: start at the newest message and emit NO history, so
      // turning a channel on does not replay a group's past into triggers.
      const d = wacli(["messages", "list", "--chat", group, "--limit", "1"], { readOnly: true });
      const newest = (d.messages || [])[0];
      process.stdout.write(JSON.stringify({ messages: [], cursor: newest ? newest.MsgID : "" }) + "\n");
      return;
    }
    // Read after a message id: the anchor's time, then everything from one
    // second before it (wacli's --after is per second), oldest first, minus
    // the anchor and anything older — the same rule apl serve's after_id uses.
    let anchor = null;
    try {
      anchor = wacli(["messages", "show", "--chat", group, "--id", req.after], { readOnly: true });
    } catch (err) {
      // ONLY a real not-found resets the cursor. wacli reports one as
      // sql.ErrNoRows (cmd/wacli/messages_helpers.go getMessageByChatFilter);
      // anything else — apl down, a locked store, a timeout — is a failed
      // receive that wfnexus must show, not a reason to skip ahead and lose
      // the messages in between.
      if (!notFound(err)) throw err;
      anchor = null;
    }
    if (!anchor || !anchor.Timestamp) {
      // The cursor's message is gone (pruned, or the store was rebuilt). Do
      // not wedge on it forever: re-read the last few messages — wfnexus
      // stores each id once, so the overlap is harmless — move the cursor to
      // the newest, and SAY so; it is shown on the System page. If this read
      // fails too, the group itself is unreadable and that is the error.
      const d = wacli(["messages", "list", "--chat", group, "--limit", String(resetWindow)], { readOnly: true });
      const recent = (d.messages || []).slice().reverse(); // oldest first
      const newest = recent.length ? recent[recent.length - 1].MsgID : "";
      process.stdout.write(JSON.stringify({
        messages: recent.filter((m) => !m.FromMe).map(toMessage),
        cursor: newest,
        notice: `cursor ${req.after} was not found in the group; re-read the last ${recent.length} message(s) and reset the cursor — anything older than that since the last poll may have been missed`,
      }) + "\n");
      return;
    }
    const since = new Date(anchor.Timestamp);
    const after = new Date(since.getTime() - 1000).toISOString().replace(/\.\d+Z$/, "Z");
    const d = wacli(["messages", "list", "--chat", group, "--after", after, "--asc", "--limit", String(limit)], { readOnly: true });
    const messages = [];
    let cursor = req.after;
    for (const m of d.messages || []) {
      if (m.MsgID === req.after || new Date(m.Timestamp) < since) continue;
      cursor = m.MsgID;
      // Our own sends (the pause announcements among them) move the cursor
      // but are not inbound messages.
      if (m.FromMe) continue;
      messages.push(toMessage(m));
    }
    process.stdout.write(JSON.stringify({ messages, cursor }) + "\n");
  } catch (err) {
    fail(`receive failed: ${err.message}`);
  }
}

module.exports = { internal };
if (require.main !== module) return;

const verb = process.argv[2];
if (verb === "send") {
  send().catch((err) => fail(`send failed: ${err.message}. The message MAY have landed — do not retry blind.`));
} else if (verb === "receive") {
  receive();
} else {
  fail(`unknown verb ${JSON.stringify(verb)} — ${VERSION} adapters take send | receive`);
}
