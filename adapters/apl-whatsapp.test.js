// Tests for the reference adapter against a fake apl (fake-apl.js).
// Run: node --test adapters/
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const { spawnSync } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const ADAPTER = path.join(__dirname, "apl-whatsapp.js");
const FAKE = path.join(__dirname, "fake-apl.js");
const G = "120300000000000000@g.us";

function setup(messages, extra = {}) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "apl-wa-test-"));
  const log = path.join(dir, "argv.log");
  const group = path.join(dir, "group.json");
  fs.writeFileSync(group, JSON.stringify(messages));
  // APL_BIN must be ONE executable; a tiny shell wrapper runs the fake.
  let bin = path.join(dir, "apl");
  if (process.platform === "win32") {
    bin += ".cmd";
    fs.writeFileSync(bin, `@node "${FAKE}" %*\r\n`);
  } else {
    fs.writeFileSync(bin, `#!/bin/sh\nexec node "${FAKE}" "$@"\n`, { mode: 0o755 });
  }
  const ledger = path.join(dir, "state", "sent.jsonl");
  const env = { ...process.env, APL_BIN: bin, FAKE_APL_LOG: log, FAKE_APL_GROUP: group, APL_WA_GROUP: G, APL_WA_LEDGER: ledger, ...extra };
  const run = (verb, stdin) => spawnSync(process.execPath, [ADAPTER, verb], { input: JSON.stringify(stdin), env, encoding: "utf8" });
  const calls = () => fs.readFileSync(log, "utf8").trim().split("\n").filter(Boolean).map((l) => JSON.parse(l));
  const calls2 = () => (fs.existsSync(log) ? calls() : []);
  return { run, calls, calls2, ledger, dir };
}

const msgs = [
  { MsgID: "A", SenderJID: "1@s.whatsapp.net", Timestamp: "2026-10-20T10:00:00Z", Text: "old" },
  { MsgID: "B", SenderJID: "2@s.whatsapp.net", SenderName: "lead", Timestamp: "2026-10-20T10:01:00Z", Text: "#ab12 yes" },
  { MsgID: "C", FromMe: true, Timestamp: "2026-10-20T10:02:00Z", Text: "our own pause message" },
  { MsgID: "D", SenderJID: "3@s.whatsapp.net", Timestamp: "2026-10-20T10:03:00Z", MediaType: "document", Filename: "r.pdf", MimeType: "application/pdf", MediaCaption: "report" },
];

test("send: text goes through apl with … wacli send text, and prints the id", () => {
  const { run, calls } = setup(msgs);
  const r = run("send", { version: "wfnexus.channel/v1", type: "pause", text: "hello" });
  assert.strictEqual(r.status, 0, r.stderr);
  assert.deepStrictEqual(JSON.parse(r.stdout), { id: "SENT1", status: "sent" });
  assert.deepStrictEqual(calls()[0], ["with", "whatsapp:biz", "--", "wacli", "--json", "send", "text", "--to", G, "--message", "hello"]);
});

test("send: target overrides the group, a path is sent as a file with the text as caption", () => {
  const { run, calls } = setup(msgs, { APL_WA_LABEL: "ops" });
  const other = "120399999999999999@g.us";
  const r = run("send", { text: "see", target: other, attachment: { path: "/tmp/x.pdf", filename: "x.pdf" } });
  assert.strictEqual(r.status, 0, r.stderr);
  const c = calls()[0];
  assert.deepStrictEqual(c.slice(0, 2), ["with", "whatsapp:ops"]);
  assert.ok(c.includes("file") && c.includes(other) && c.includes("/tmp/x.pdf") && c.includes("--caption"), c.join(" "));
});

test("send: a failure exits non-zero and says not to retry blind", () => {
  const { run } = setup(msgs, { FAKE_APL_FAIL: "not connected" });
  const r = run("send", { text: "hello" });
  assert.notStrictEqual(r.status, 0);
  assert.match(r.stderr, /not connected/);
  assert.strictEqual(JSON.parse(r.stdout).status, "failed");
  assert.match(r.stderr, /do not retry blind/);
});

test("receive: first poll starts at the newest message and emits no history", () => {
  const { run } = setup(msgs);
  const r = run("receive", { version: "wfnexus.channel/v1", after: "" });
  assert.strictEqual(r.status, 0, r.stderr);
  assert.deepStrictEqual(JSON.parse(r.stdout), { messages: [], cursor: "D" });
});

test("receive: after a cursor, oldest first; an own message we did not send is kept as fromOwner", () => {
  const { run, calls } = setup(msgs);
  const r = run("receive", { after: "A" });
  assert.strictEqual(r.status, 0, r.stderr);
  const out = JSON.parse(r.stdout);
  assert.strictEqual(out.cursor, "D");
  assert.deepStrictEqual(out.messages.map((m) => m.id), ["B", "C", "D"]);
  assert.deepStrictEqual(out.messages[0], { id: "B", from: "2@s.whatsapp.net", name: "lead", text: "#ab12 yes", at: "2026-10-20T10:01:00Z", chat: G, chatType: "group", kind: "message" });
  assert.strictEqual(out.messages[1].fromOwner, true);
  out.messages.splice(1, 1);
  assert.deepStrictEqual(out.messages[1].attachments, [{ filename: "r.pdf", mime: "application/pdf" }]);
  assert.strictEqual(out.messages[1].text, "report");
  for (const c of calls()) assert.ok(c.includes("--read-only"), "reads must be read-only: " + c.join(" "));
  assert.ok(!calls().some((c) => c.includes("sync")), "the adapter must never sync");
});

test("receive: a lost cursor does not wedge the channel — it re-reads recent messages, resets, and says so", () => {
  const { run } = setup(msgs);
  const r = run("receive", { after: "ZZZ" });
  assert.strictEqual(r.status, 0, r.stderr);
  const out = JSON.parse(r.stdout);
  assert.strictEqual(out.cursor, "D");
  assert.deepStrictEqual(out.messages.map((m) => m.id), ["A", "B", "C", "D"]);
  assert.match(out.notice, /ZZZ was not found/);
});

test("receive: an unreadable group is an error, not an empty inbox", () => {
  const { run } = setup(msgs, { FAKE_APL_GROUP: "/nonexistent/group.json" });
  const r = run("receive", { after: "A" });
  assert.notStrictEqual(r.status, 0);
  assert.match(r.stderr, /receive failed/);
});

test("send: an attachment URL that is not https, or points inside the network, is refused before any fetch", () => {
  for (const url of ["http://example.com/x.pdf", "https://127.0.0.1/x", "https://169.254.169.254/latest", "https://10.1.2.3/x", "https://[::1]/x", "https://localhost/x"]) {
    const { run, calls } = setup(msgs);
    const r = run("send", { text: "x", attachment: { url } });
    assert.notStrictEqual(r.status, 0, url);
    assert.match(r.stderr, /https|internal/, url);
    assert.throws(() => calls(), undefined, "nothing may be sent for " + url);
  }
});

// ---------- ADR 0026: inbox mode, full event contract, echo ledger, sender ----------

const D1 = "15550100001@s.whatsapp.net";
const inboxRows = [
  { ChatJID: G, MsgID: "I1", SenderJID: "1@s.whatsapp.net", Timestamp: "2026-11-01T10:00:00Z", Text: "start" },
  { ChatJID: D1, ChatName: "Asha", MsgID: "I2", SenderJID: "", SenderName: "Asha", Timestamp: "2026-11-01T10:01:00Z", Text: "hi in a DM" },
  { ChatJID: G, ChatName: "Ops", MsgID: "I3", SenderJID: "2@s.whatsapp.net", Timestamp: "2026-11-01T10:02:00Z", Text: "@15550100009 see this", quoted_msg_id: "I1", quoted_sender_jid: "1@s.whatsapp.net" },
  { ChatJID: G, MsgID: "I4", SenderJID: "3@s.whatsapp.net", Timestamp: "2026-11-01T10:03:00Z", ReactionToID: "I3", ReactionEmoji: "👍" },
  { ChatJID: D1, MsgID: "I5", SenderJID: D1, Timestamp: "2026-11-01T10:04:00Z", MediaType: "ptt", MimeType: "audio/ogg; codecs=opus", FileLength: 4096 },
  { ChatJID: G, MsgID: "I6", SenderJID: "2@s.whatsapp.net", Timestamp: "2026-11-01T10:05:00Z", Text: "fixed typo", Edited: true },
  { ChatJID: G, MsgID: "I7", SenderJID: "2@s.whatsapp.net", Timestamp: "2026-11-01T10:06:00Z", Revoked: true },
  { ChatJID: G, MsgID: "ECHO", FromMe: true, Timestamp: "2026-11-01T10:07:00Z", Text: "our bot reply" },
  { ChatJID: D1, MsgID: "PHONE", FromMe: true, SenderJID: "me@s.whatsapp.net", Timestamp: "2026-11-01T10:08:00Z", Text: "typed on the phone" },
];

test("inbox: unset APL_WA_GROUP reads every chat, no --chat filter, cursor is chat|id", () => {
  const { run, calls } = setup(inboxRows, { APL_WA_GROUP: "" });
  const r = run("receive", { after: "" });
  assert.strictEqual(r.status, 0, r.stderr);
  assert.deepStrictEqual(JSON.parse(r.stdout), { messages: [], cursor: D1 + "|PHONE" });
  assert.ok(!calls()[0].includes("--chat"));
});

test("inbox: emits the full event contract across DMs and groups", () => {
  const { run, ledger } = setup(inboxRows, { APL_WA_GROUP: "*" });
  fs.mkdirSync(path.dirname(ledger), { recursive: true });
  fs.writeFileSync(ledger, JSON.stringify({ id: "ECHO", chat: G, at: new Date().toISOString() }) + "\n");
  const r = run("receive", { after: G + "|I1" });
  assert.strictEqual(r.status, 0, r.stderr);
  const out = JSON.parse(r.stdout);
  const by = Object.fromEntries(out.messages.map((m) => [m.id, m]));
  assert.deepStrictEqual(out.messages.map((m) => m.id), ["I2", "I3", "I4", "I5", "I6", "I7", "PHONE"], "ECHO is dropped (in the ledger)");
  assert.strictEqual(out.cursor, D1 + "|PHONE");
  assert.deepStrictEqual([by.I2.chatType, by.I2.from, by.I2.chatName], ["dm", D1, "Asha"]);
  assert.deepStrictEqual(by.I3.replyTo, { id: "I1", sender: "1@s.whatsapp.net", text: "start" });
  assert.deepStrictEqual(by.I3.mentions, ["15550100009@s.whatsapp.net"]);
  assert.deepStrictEqual([by.I4.kind, by.I4.reaction], ["reaction", { targetId: "I3", emoji: "👍" }]);
  assert.deepStrictEqual(by.I5.media, { kind: "voice", mime: "audio/ogg; codecs=opus", name: "", size: 4096 });
  assert.strictEqual(by.I6.kind, "updated");
  assert.strictEqual(by.I7.kind, "deleted");
  assert.strictEqual(by.PHONE.fromOwner, true);
  assert.strictEqual(by.I3.fromOwner, undefined);
});

test("single chat: a DM JID works as APL_WA_GROUP and only that chat is read", () => {
  const { run, calls } = setup(inboxRows, { APL_WA_GROUP: D1 });
  const r = run("receive", { after: "I2" });
  assert.strictEqual(r.status, 0, r.stderr);
  const out = JSON.parse(r.stdout);
  assert.deepStrictEqual(out.messages.map((m) => m.id), ["I5", "PHONE"]);
  assert.strictEqual(out.cursor, "PHONE");
  for (const c of calls()) assert.ok(c.includes("--chat") && c.includes(D1));
});

test("echo: a sent id is in the ledger before it is reported, and the next receive drops it", () => {
  const rows = [{ ChatJID: G, MsgID: "A0", SenderJID: "1@s.whatsapp.net", Timestamp: "2026-11-01T09:00:00Z", Text: "x" },
    { ChatJID: G, MsgID: "SENT1", FromMe: true, Timestamp: "2026-11-01T09:01:00Z", Text: "hello" }];
  const { run, ledger } = setup(rows);
  const s = run("send", { text: "hello" });
  assert.strictEqual(s.status, 0, s.stderr);
  assert.match(fs.readFileSync(ledger, "utf8"), /"id":"SENT1"/);
  const r = run("receive", { after: "A0" });
  const out = JSON.parse(r.stdout);
  assert.deepStrictEqual(out.messages, []);
  assert.strictEqual(out.cursor, "SENT1");
});

test("send: inbox mode needs a target", () => {
  const { run, calls2 } = setup(msgs, { APL_WA_GROUP: "*" });
  const r = run("send", { text: "hi" });
  assert.notStrictEqual(r.status, 0);
  assert.strictEqual(JSON.parse(r.stdout).status, "failed");
  assert.deepStrictEqual(calls2(), []);
});

test("send: reply-to with sender, and mentions", () => {
  const { run, calls } = setup(msgs, { APL_WA_GROUP: "*" });
  const r = run("send", { text: "@15550100009 ok", target: G, replyTo: { id: "B", sender: "2@s.whatsapp.net" }, mentions: ["15550100009@s.whatsapp.net"] });
  assert.strictEqual(r.status, 0, r.stderr);
  assert.deepStrictEqual(calls()[0].slice(5), ["send", "text", "--to", G, "--message", "@15550100009 ok", "--reply-to", "B", "--reply-to-sender", "2@s.whatsapp.net", "--mention", "15550100009@s.whatsapp.net"]);
});

test("send: image, voice and sticker pick the right wacli command", () => {
  for (const [kind, want] of [["image", ["send", "file"]], ["voice", ["send", "voice"]], ["sticker", ["send", "sticker"]]]) {
    const { run, calls } = setup(msgs);
    const r = run("send", { text: "", attachment: { path: "/tmp/a", kind } });
    assert.strictEqual(r.status, 0, kind + ": " + r.stderr);
    assert.deepStrictEqual(calls()[0].slice(5, 7), want, kind);
    if (kind === "image") assert.ok(calls()[0].includes("--as"));
  }
});

test("send: an attachment is uncertain unless wacli confirms the file", () => {
  const { run } = setup(msgs, { FAKE_APL_NOFILE: "1" });
  const r = run("send", { text: "see", attachment: { path: "/tmp/x.pdf" } });
  assert.notStrictEqual(r.status, 0);
  assert.strictEqual(JSON.parse(r.stdout).status, "uncertain");
  assert.match(r.stderr, /MAY have landed/);
});

test("send: no parseable answer from wacli is uncertain, not failed", () => {
  const { run, ledger } = setup(msgs, { FAKE_APL_GARBAGE: "1" });
  const r = run("send", { text: "hi" });
  assert.notStrictEqual(r.status, 0);
  assert.strictEqual(JSON.parse(r.stdout).status, "uncertain");
  assert.ok(!fs.existsSync(ledger));
});

test("send: reaction, edit and delete of our own message", () => {
  let t = setup(msgs);
  let r = t.run("send", { text: "", reaction: { targetId: "B", emoji: "✅", sender: "2@s.whatsapp.net" } });
  assert.strictEqual(r.status, 0, r.stderr);
  assert.deepStrictEqual(t.calls()[0].slice(5), ["send", "react", "--to", G, "--id", "B", "--reaction", "✅", "--sender", "2@s.whatsapp.net"]);
  t = setup(msgs);
  r = t.run("send", { text: "new words", edit: { id: "SENT9" } });
  assert.strictEqual(r.status, 0, r.stderr);
  assert.deepStrictEqual(t.calls()[0].slice(5), ["messages", "edit", "--chat", G, "--id", "SENT9", "--message", "new words"]);
  assert.deepStrictEqual(JSON.parse(r.stdout), { id: "SENT9", status: "sent" });
  t = setup(msgs);
  r = t.run("send", { text: "", delete: { id: "SENT9" } });
  assert.strictEqual(r.status, 0, r.stderr);
  assert.deepStrictEqual(t.calls()[0].slice(5), ["messages", "revoke", "--chat", G, "--id", "SENT9"]);
});

