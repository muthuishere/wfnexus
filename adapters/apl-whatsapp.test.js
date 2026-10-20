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
  const env = { ...process.env, APL_BIN: bin, FAKE_APL_LOG: log, FAKE_APL_GROUP: group, APL_WA_GROUP: G, ...extra };
  const run = (verb, stdin) => spawnSync(process.execPath, [ADAPTER, verb], { input: JSON.stringify(stdin), env, encoding: "utf8" });
  const calls = () => fs.readFileSync(log, "utf8").trim().split("\n").filter(Boolean).map((l) => JSON.parse(l));
  return { run, calls };
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
  assert.deepStrictEqual(JSON.parse(r.stdout), { id: "SENT1" });
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
  assert.match(r.stderr, /MAY have landed/);
  assert.match(r.stderr, /do not retry blind/);
});

test("receive: first poll starts at the newest message and emits no history", () => {
  const { run } = setup(msgs);
  const r = run("receive", { version: "wfnexus.channel/v1", after: "" });
  assert.strictEqual(r.status, 0, r.stderr);
  assert.deepStrictEqual(JSON.parse(r.stdout), { messages: [], cursor: "D" });
});

test("receive: after a cursor, oldest first, own messages skipped but passed by the cursor", () => {
  const { run, calls } = setup(msgs);
  const r = run("receive", { after: "A" });
  assert.strictEqual(r.status, 0, r.stderr);
  const out = JSON.parse(r.stdout);
  assert.strictEqual(out.cursor, "D");
  assert.deepStrictEqual(out.messages.map((m) => m.id), ["B", "D"]);
  assert.deepStrictEqual(out.messages[0], { id: "B", from: "2@s.whatsapp.net", name: "lead", text: "#ab12 yes", at: "2026-10-20T10:01:00Z" });
  assert.deepStrictEqual(out.messages[1].attachments, [{ filename: "r.pdf", mime: "application/pdf" }]);
  assert.strictEqual(out.messages[1].text, "report");
  for (const c of calls()) assert.ok(c.includes("--read-only"), "reads must be read-only: " + c.join(" "));
  assert.ok(!calls().some((c) => c.includes("sync")), "the adapter must never sync");
});

test("receive: an unknown cursor is an error, not an empty inbox", () => {
  const { run } = setup(msgs);
  const r = run("receive", { after: "ZZZ" });
  assert.notStrictEqual(r.status, 0);
  assert.match(r.stderr, /receive failed/);
});
