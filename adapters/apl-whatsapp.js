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
//   APL_WA_TIMEOUT_MS how long one apl call may take; default 120000
//   APL_WA_LIMIT      most messages one receive returns; default 200

"use strict";

const { spawnSync } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const VERSION = "wfnexus.channel/v1";
const env = process.env;
const label = env.APL_WA_LABEL || "biz";
const apl = env.APL_BIN || "apl";
const timeout = Number(env.APL_WA_TIMEOUT_MS || 120000);
const limit = Number(env.APL_WA_LIMIT || 200);

function fail(msg) {
  process.stderr.write("apl-whatsapp: " + msg + "\n");
  process.exit(1);
}

// wacli runs through apl, with --json; its answer is {"success","data","error"}.
function wacli(args, { readOnly } = {}) {
  const argv = ["with", "whatsapp:" + label];
  if (readOnly) argv.push("--read-only");
  argv.push("--", "wacli", "--json", ...args);
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

// fetchToFile downloads a URL attachment, because wacli sends files, not URLs.
async function fetchToFile(url, filename) {
  const res = await fetch(url);
  if (!res.ok) throw new Error(`attachment ${url}: HTTP ${res.status}`);
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "wfx-wa-"));
  const name = filename || path.basename(new URL(url).pathname) || "attachment";
  const file = path.join(dir, name);
  fs.writeFileSync(file, Buffer.from(await res.arrayBuffer()));
  return file;
}

async function send() {
  const e = readStdin();
  const group = groupOf(e);
  const text = e.text || "";
  let data;
  try {
    const a = e.attachment;
    if (a && (a.path || a.url)) {
      const file = a.path || (await fetchToFile(a.url, a.filename));
      const args = ["send", "file", "--to", group, "--file", file];
      if (a.filename) args.push("--filename", a.filename);
      if (text) args.push("--caption", text);
      data = wacli(args);
    } else {
      if (!text) fail("nothing to send: the envelope has no text and no attachment");
      data = wacli(["send", "text", "--to", group, "--message", text]);
    }
  } catch (err) {
    // NEVER retried, here or by wfnexus: a send that errored or timed out may
    // still have gone out, and a duplicate in a group is worse than an error.
    fail(`send failed: ${err.message}. The message MAY have landed — read the group before sending it again; do not retry blind.`);
  }
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
    const anchor = wacli(["messages", "show", "--chat", group, "--id", req.after], { readOnly: true });
    if (!anchor || !anchor.Timestamp) throw new Error(`cursor message ${req.after} not found in that group`);
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

const verb = process.argv[2];
if (verb === "send") {
  send().catch((err) => fail(`send failed: ${err.message}. The message MAY have landed — do not retry blind.`));
} else if (verb === "receive") {
  receive();
} else {
  fail(`unknown verb ${JSON.stringify(verb)} — ${VERSION} adapters take send | receive`);
}
