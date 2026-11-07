#!/usr/bin/env node
// apl-whatsapp — the reference wfnexus channel adapter (wfnexus.channel/v1),
// the WhatsApp monitor + sender of ADR 0026 ("WhatsApp coverage").
//
// wfnexus runs this as a channel's `run:` and appends a verb:
//
//   node adapters/apl-whatsapp.js send     stdin: envelope   stdout: {"id","status":"sent"}
//   node adapters/apl-whatsapp.js receive  stdin: {"after"}  stdout: {"messages": [...], "cursor": "..."}
//
// The contract is docs/channels.md; the JSON Schemas are docs/schemas/. Nothing
// in wfnexus knows this file exists.
//
// HOW IT REACHES WHATSAPP: through `apl with whatsapp:<label> -- wacli --json …`.
// apl maps the label to a wacli account; wacli does the protocol. Receive is a
// poll of wacli's local store (`--read-only`), which the account's live
// `wacli sync --follow` (apl serve) keeps current. Plain Node, no dependencies,
// Node 18+.
//
// SINGLE WRITER. One WhatsApp number must have one writer. Where a live syncer
// owns the account, `wacli send` delegates to its send socket (which also paces
// sends). Run this adapter on the machine that owns the account. It never runs
// `wacli sync`, and it never retries a send.
//
// MODES
//   inbox   APL_WA_GROUP unset or "*": every chat the account sees, groups AND
//           DMs. Each event carries `chat` and `chatType`; the cursor is
//           "<chat>|<id>". A send needs the envelope's `target`.
//   single  APL_WA_GROUP=<jid> (a group …@g.us or a DM …@s.whatsapp.net / @lid):
//           only that chat; the cursor is a bare message id (the pre-0026
//           format, so existing channels keep their cursor).
//
// ECHO LEDGER. Every id this adapter sends is appended to a sent-id ledger
// BEFORE it is reported sent. Receive drops a FromMe message whose id is in the
// ledger (our own echo) and KEEPS any other FromMe message as `fromOwner: true`
// (the owner typed it on the phone). The ledger is a JSON-lines file
// ({"id","chat","at"} per line, entries older than 30 days pruned):
//   APL_WA_LEDGER            the file, if set; else
//   WFX_CHANNEL_STATE_DIR    a per-channel state dir wfnexus may provide
//                            → <dir>/apl-whatsapp-<label>.sent.jsonl; else
//   ~/.local/state/wfnexus/apl-whatsapp-<label>.sent.jsonl
// Send and receive for one account MUST resolve to the same file. A send that
// could not write the ledger still reports sent (it went out) with a warning;
// its echo will then surface as fromOwner.
//
// Environment (set them in the channel's `env:` — values may be ${VAR}):
//
//   APL_WA_GROUP      "*"/unset = inbox mode; a JID = that chat only.
//                     The envelope's `target` overrides it for a send.
//   APL_WA_LABEL      the apl handle label (whatsapp:<label>); default "biz"
//   APL_BIN           the apl binary; default "apl"
//   APL_WA_LEDGER     see ECHO LEDGER
//   APL_WA_TIMEOUT_MS the most one invocation may take; default 120000, and
//                     never longer than WFX_CHANNEL_TIMEOUT_MS less 5s
//   APL_WA_LIMIT      most messages one receive returns; default 200
//   APL_WA_RESET_WINDOW  messages re-read when the cursor is lost; default 20
//
// NOT YET (follow-up): the push listener on `apl serve /subscribe`. The store
// poll sees an edit or a delete only as the Edited/Revoked flag on a row that
// is still past the cursor; edits/deletes of older messages need the push feed.
// Media bytes are not fetched here — `media` is metadata; fetch on demand.

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
const chatEnv = (env.APL_WA_GROUP || "").trim();
const inbox = chatEnv === "" || chatEnv === "*";
const LEDGER_TTL_MS = 30 * 24 * 3600 * 1000;

let budget = Number(env.APL_WA_TIMEOUT_MS || 120000);
if (env.WFX_CHANNEL_TIMEOUT_MS) budget = Math.min(budget, Math.max(1000, Number(env.WFX_CHANNEL_TIMEOUT_MS) - 5000));
const deadline = Date.now() + budget;
const remaining = () => Math.max(1000, deadline - Date.now());

function fail(msg) {
  process.stderr.write("apl-whatsapp: " + msg + "\n");
  process.exit(1);
}

// A send that did not report sent: stdout carries the status, stderr the why.
function sendFail(status, msg) {
  process.stdout.write(JSON.stringify({ status, error: msg }) + "\n");
  const tail = status === "uncertain"
    ? " The message MAY have landed — read the chat before sending it again; do not retry blind."
    : " Nothing was confirmed sent; do not retry blind.";
  fail(`send ${status}: ${msg}.${tail}`);
}

class WacliError extends Error {
  constructor(msg, uncertain) {
    super(msg);
    this.uncertain = uncertain; // true: we cannot tell whether it happened
  }
}

// wacli runs through apl, with --json; its answer is {"success","data","error"}.
function wacli(args, { readOnly } = {}) {
  const argv = ["with", "whatsapp:" + label];
  if (readOnly) argv.push("--read-only");
  argv.push("--", "wacli", "--json", ...args);
  const timeout = remaining();
  const r = spawnSync(apl, argv, { encoding: "utf8", timeout, maxBuffer: 64 << 20 });
  if (r.error) {
    if (r.error.code === "ETIMEDOUT") throw new WacliError(`apl timed out after ${timeout}ms`, true);
    throw new WacliError(`could not run ${apl}: ${r.error.message}`, false);
  }
  let body = null;
  try {
    body = JSON.parse(r.stdout || "");
  } catch (_) {
    /* fall through */
  }
  if (body && body.success) return body.data;
  if (body && body.error) throw new WacliError(String(body.error), false);
  // No parseable answer: wacli may have died mid-send.
  throw new WacliError((r.stderr || "").trim() || `exit ${r.status}`, true);
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

// ---------- ledger ----------

function ledgerPath() {
  if (env.APL_WA_LEDGER) return env.APL_WA_LEDGER;
  const dir = env.WFX_CHANNEL_STATE_DIR || path.join(os.homedir(), ".local", "state", "wfnexus");
  return path.join(dir, `apl-whatsapp-${label}.sent.jsonl`);
}

function readLedger() {
  const ids = new Set();
  let raw = "";
  try {
    raw = fs.readFileSync(ledgerPath(), "utf8");
  } catch (_) {
    return ids;
  }
  for (const line of raw.split("\n")) {
    if (!line.trim()) continue;
    try {
      const e = JSON.parse(line);
      if (e && e.id) ids.add(String(e.id));
    } catch (_) {
      /* a torn line is skipped */
    }
  }
  return ids;
}

function recordSent(id, chat) {
  const file = ledgerPath();
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.appendFileSync(file, JSON.stringify({ id, chat, at: new Date().toISOString() }) + "\n");
  // Prune now and then (when the file has grown), keeping recent entries.
  try {
    if (fs.statSync(file).size > 256 << 10) {
      const cutoff = Date.now() - LEDGER_TTL_MS;
      const keep = fs.readFileSync(file, "utf8").split("\n").filter((l) => {
        try {
          return l.trim() && new Date(JSON.parse(l).at).getTime() >= cutoff;
        } catch (_) {
          return false;
        }
      });
      const tmp = file + ".tmp" + process.pid;
      fs.writeFileSync(tmp, keep.join("\n") + "\n");
      fs.renameSync(tmp, file);
    }
  } catch (_) {
    /* pruning is best effort */
  }
}

// ---------- send ----------

function internal(ip) {
  if (net.isIPv4(ip)) {
    const [a, b] = ip.split(".").map(Number);
    return a === 0 || a === 10 || a === 127 || (a === 100 && b >= 64 && b < 128) ||
      (a === 169 && b === 254) || (a === 172 && b >= 16 && b < 32) || (a === 192 && b === 168) || a >= 224;
  }
  const v = ip.toLowerCase();
  if (v.startsWith("::ffff:")) return internal(v.slice(7));
  return v === "::" || v === "::1" || v.startsWith("fe8") || v.startsWith("fe9") || v.startsWith("fea") ||
    v.startsWith("feb") || v.startsWith("fc") || v.startsWith("fd") || v.startsWith("ff");
}

async function checkURL(raw) {
  const u = new URL(raw);
  if (u.protocol !== "https:") throw new Error(`attachment url must be https: ${raw}`);
  const host = u.hostname.replace(/^\[|\]$/g, "");
  const addrs = net.isIP(host) ? [{ address: host }] : await dns.lookup(host, { all: true });
  for (const a of addrs) {
    if (internal(a.address)) throw new Error(`attachment url ${u.hostname} resolves to an internal address (${a.address}); refused`);
  }
}

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

function targetOf(e) {
  const t = e.target || (inbox ? "" : chatEnv);
  if (!t) sendFail("failed", "no target: in inbox mode the envelope's `target` must name the chat JID");
  return t;
}

function replyArgs(e) {
  const r = e.replyTo;
  if (!r || !r.id) return [];
  const a = ["--reply-to", String(r.id)];
  if (r.sender) a.push("--reply-to-sender", String(r.sender));
  return a;
}

// buildSend turns an envelope into one wacli invocation and the rule that
// says whether its answer confirms the action.
function buildSend(e, to, file) {
  if (e.reaction && e.reaction.targetId) {
    const r = e.reaction;
    const args = ["send", "react", "--to", to, "--id", String(r.targetId), "--reaction", String(r.emoji ?? "")];
    if (r.sender) args.push("--sender", String(r.sender));
    return { args, ok: (d) => d.sent === true };
  }
  if (e.edit && e.edit.id) {
    if (!e.text) sendFail("failed", "edit needs the new text in `text`");
    return { args: ["messages", "edit", "--chat", to, "--id", String(e.edit.id), "--message", e.text], ok: (d) => d.edited === true, id: (d) => d.id || e.edit.id };
  }
  if (e.delete && e.delete.id) {
    return { args: ["messages", "revoke", "--chat", to, "--id", String(e.delete.id)], ok: (d) => d.revoked === true, id: (d) => d.id || e.delete.id };
  }
  if (file) {
    const a = e.attachment || {};
    const kind = a.kind || "file";
    let args;
    if (kind === "voice") args = ["send", "voice", "--to", to, "--file", file];
    else if (kind === "sticker") args = ["send", "sticker", "--to", to, "--file", file];
    else {
      args = ["send", "file", "--to", to, "--file", file];
      if (kind === "image") args.push("--as", "image");
      if (a.filename) args.push("--filename", a.filename);
      if (e.text) args.push("--caption", e.text);
    }
    args.push(...replyArgs(e));
    // No silent media loss (ADR 0026): `sent` only when wacli says sent AND
    // reports the file it attached.
    const needsFile = kind !== "sticker";
    return { args, ok: (d) => d.sent === true && (!needsFile || (d.file && typeof d.file === "object")), media: true };
  }
  if (!e.text) sendFail("failed", "nothing to send: the envelope has no text and no attachment");
  const args = ["send", "text", "--to", to, "--message", e.text, ...replyArgs(e)];
  for (const m of e.mentions || []) args.push("--mention", String(m));
  return { args, ok: (d) => d.sent === true };
}

async function send() {
  const e = readStdin();
  const to = targetOf(e);
  let file = null;
  let cleanup = () => {};
  const a = e.attachment;
  if (a && (a.path || a.url) && !e.reaction && !e.edit && !e.delete) {
    if (a.path) file = a.path;
    else {
      try {
        ({ file, cleanup } = await fetchToFile(a.url, a.filename));
      } catch (err) {
        sendFail("failed", err.message);
      }
    }
  }
  const plan = buildSend(e, to, file);
  let data;
  try {
    data = wacli(plan.args);
  } catch (err) {
    cleanup();
    // NEVER retried: an error after the request left may still have delivered.
    sendFail(err.uncertain ? "uncertain" : "failed", err.message);
  }
  cleanup();
  if (!data || !plan.ok(data)) {
    sendFail("uncertain", plan.media
      ? "wacli did not confirm the attachment was sent"
      : "wacli did not confirm the send");
  }
  const id = String((plan.id ? plan.id(data) : data.id) || "");
  const out = { id, status: "sent" };
  if (id) {
    try {
      recordSent(id, to); // BEFORE we report sent
    } catch (err) {
      out.warning = `sent, but the echo ledger could not be written (${err.message}); its echo will look like an owner message`;
      process.stderr.write("apl-whatsapp: " + out.warning + "\n");
    }
  }
  process.stdout.write(JSON.stringify(out) + "\n");
}

// ---------- receive ----------

const MEDIA_KINDS = { image: "image", sticker: "sticker", video: "video", gif: "video", audio: "audio", ptt: "voice", voice: "voice", document: "document" };

function mediaKind(mt, mime) {
  const k = MEDIA_KINDS[String(mt || "").toLowerCase()] || "document";
  if (k === "audio" && /opus|^audio\/ogg/.test(mime || "")) return "voice";
  return k;
}

// WhatsApp puts mentions in the text as @<number>; the store keeps no list, so
// they are read back from the text.
function mentionsOf(text) {
  const out = [];
  for (const m of String(text || "").matchAll(/@(\d{6,16})\b/g)) {
    const j = m[1] + "@s.whatsapp.net";
    if (!out.includes(j)) out.push(j);
  }
  return out;
}

// toEvent maps a wacli store row (store.Message: ChatJID, ChatName, MsgID,
// SenderJID, SenderName, Timestamp, FromMe, Text, DisplayText, quoted_msg_id,
// quoted_sender_jid, ReactionToID, ReactionEmoji, MediaType, MediaCaption,
// Filename, MimeType, Edited, Revoked, DeletedForMe) onto the event contract.
function toEvent(m, chatHint, byId) {
  const chat = m.ChatJID || chatHint || "";
  const chatType = chat.endsWith("@g.us") ? "group" : "dm";
  let from = m.SenderJID || "";
  if (!from && chatType === "dm" && !m.FromMe) from = chat;
  const text = m.Text || m.MediaCaption || m.DisplayText || "";
  let kind = "message";
  if (m.Revoked || m.DeletedForMe) kind = "deleted";
  else if (m.ReactionToID) kind = "reaction";
  else if (m.Edited) kind = "updated";
  const out = { id: m.MsgID, from, name: m.SenderName || "", text: kind === "reaction" ? "" : text, at: m.Timestamp, chat, chatType, kind };
  if (m.ChatName) out.chatName = m.ChatName;
  if (m.FromMe) out.fromOwner = true;
  if (kind === "reaction") out.reaction = { targetId: m.ReactionToID, emoji: m.ReactionEmoji || "" };
  const qid = m.quoted_msg_id || m.QuotedMsgID;
  if (qid) {
    const q = byId.get(qid);
    out.replyTo = { id: qid, sender: m.quoted_sender_jid || m.QuotedSenderJID || "" };
    const qt = m.ReplyToDisplay || (q && (q.Text || q.MediaCaption || q.DisplayText));
    if (qt) out.replyTo.text = qt;
  }
  if (m.MediaType && kind !== "reaction") {
    out.media = { kind: mediaKind(m.MediaType, m.MimeType), mime: m.MimeType || "", name: m.Filename || "" };
    const size = Number(m.FileLength || m.Size || 0);
    if (size > 0) out.media.size = size;
    out.attachments = [{ filename: m.Filename || "", mime: m.MimeType || "" }]; // the v1 field, kept
  }
  const mentions = mentionsOf(text);
  if (mentions.length) out.mentions = mentions;
  return out;
}

// parseCursor: inbox cursors are "<chat>|<id>"; a bare id belongs to the
// configured chat (single mode, and every pre-0026 cursor).
function parseCursor(c) {
  const i = c.lastIndexOf("|");
  if (i > 0) return { chat: c.slice(0, i), id: c.slice(i + 1) };
  return { chat: inbox ? "" : chatEnv, id: c };
}

function cursorOf(m) {
  return inbox ? `${m.ChatJID || ""}|${m.MsgID}` : m.MsgID;
}

function list(extra) {
  const args = ["messages", "list"];
  if (!inbox) args.push("--chat", chatEnv);
  return (wacli([...args, ...extra], { readOnly: true }).messages) || [];
}

function receive() {
  const req = readStdin();
  if (req.target && !inbox && req.target !== chatEnv) fail(`target ${req.target} does not match APL_WA_GROUP`);
  try {
    if (!req.after) {
      // First poll: start at the newest message and emit NO history.
      const newest = list(["--limit", "1"])[0];
      process.stdout.write(JSON.stringify({ messages: [], cursor: newest ? cursorOf(newest) : "" }) + "\n");
      return;
    }
    const ledger = readLedger();
    const emit = (rows, context = []) => {
      const byId = new Map([...context, ...rows].map((m) => [m.MsgID, m]));
      const out = [];
      for (const m of rows) {
        if (!m.MsgID) continue;
        if (m.FromMe && ledger.has(m.MsgID)) continue; // our own echo
        if (m.ReactionToID && !m.ReactionEmoji && !m.Revoked) continue; // an un-react
        out.push(toEvent(m, inbox ? "" : chatEnv, byId));
      }
      return out;
    };
    const cur = parseCursor(req.after);
    let anchor = null;
    if (cur.chat) {
      try {
        anchor = wacli(["messages", "show", "--chat", cur.chat, "--id", cur.id], { readOnly: true });
      } catch (_) {
        anchor = null;
      }
    }
    if (!anchor || !anchor.Timestamp) {
      // The cursor's message is gone: re-read a recent window (duplicates are
      // harmless), reset, and SAY so. If this read fails, that is the error.
      const recent = list(["--limit", String(resetWindow)]).slice().reverse();
      const newest = recent.length ? cursorOf(recent[recent.length - 1]) : "";
      process.stdout.write(JSON.stringify({
        messages: emit(recent),
        cursor: newest,
        notice: `cursor ${req.after} was not found; re-read the last ${recent.length} message(s) and reset the cursor — anything older than that since the last poll may have been missed`,
      }) + "\n");
      return;
    }
    // From one second before the anchor (wacli's --after is per second),
    // oldest first, minus the anchor and anything older.
    const since = new Date(anchor.Timestamp);
    const after = new Date(since.getTime() - 1000).toISOString().replace(/\.\d+Z$/, "Z");
    const rows = [];
    let cursor = req.after;
    const page = list(["--after", after, "--asc", "--limit", String(limit)]);
    for (const m of page) {
      if (m.MsgID === cur.id || new Date(m.Timestamp) < since) continue;
      cursor = cursorOf(m);
      rows.push(m);
    }
    process.stdout.write(JSON.stringify({ messages: emit(rows, [anchor, ...page]), cursor }) + "\n");
  } catch (err) {
    fail(`receive failed: ${err.message}`);
  }
}

// self prints the ids that are US on this account, {"self": [jid, ...]}, so
// wfnexus can tell a group message that mentions or replies to us (optional
// verb; read-only: `wacli auth status`).
function self() {
  try {
    const d = wacli(["auth", "status"], { readOnly: true }) || {};
    const ids = [d.linked_jid, d.lid].filter((x) => typeof x === "string" && x);
    if (d.phone && !ids.length) ids.push(String(d.phone) + "@s.whatsapp.net");
    // A device JID ("123:4@s.whatsapp.net") is mentioned as "123@s.whatsapp.net".
    const plain = ids.map((j) => j.replace(/:\d+@/, "@"));
    process.stdout.write(JSON.stringify({ self: [...new Set([...plain, ...ids])] }) + "\n");
  } catch (err) {
    fail(`self failed: ${err.message}`);
  }
}

const verb = process.argv[2];
if (verb === "send") {
  send().catch((err) => sendFail("uncertain", err.message));
} else if (verb === "receive") {
  receive();
} else if (verb === "self") {
  self();
} else {
  fail(`unknown verb ${JSON.stringify(verb)} — ${VERSION} adapters take send | receive | self`);
}
