#!/usr/bin/env node
// fake-apl — a stand-in for `apl with whatsapp:<label> -- wacli --json …`, used
// by apl-whatsapp.test.js. It records every argv to $FAKE_APL_LOG and answers
// from $FAKE_APL_GROUP (a JSON array of wacli store rows, oldest first; rows
// without ChatJID belong to every chat) in wacli's own {"success","data"} shape.
//   FAKE_APL_FAIL     sends answer {"success":false,"error":<value>}
//   FAKE_APL_GARBAGE  sends print non-JSON and exit 1 (an unknown outcome)
//   FAKE_APL_NOFILE   file/voice sends answer sent:true but no `file`
"use strict";
const fs = require("node:fs");
const argv = process.argv.slice(2);
fs.appendFileSync(process.env.FAKE_APL_LOG, JSON.stringify(argv) + "\n");
const w = argv.slice(argv.indexOf("--") + 1); // wacli --json <cmd…>
const flag = (n) => (w.indexOf(n) >= 0 ? w[w.indexOf(n) + 1] : undefined);
const group = JSON.parse(fs.readFileSync(process.env.FAKE_APL_GROUP, "utf8"));
const ok = (data) => process.stdout.write(JSON.stringify({ success: true, data }) + "\n");
const cmd = w.slice(2, 4).join(" ");
const inChat = (m) => !flag("--chat") || !m.ChatJID || m.ChatJID === flag("--chat");
const writes = ["send text", "send file", "send voice", "send sticker", "send react", "messages edit", "messages revoke"];
if (writes.includes(cmd)) {
  if (process.env.FAKE_APL_FAIL) {
    process.stdout.write(JSON.stringify({ success: false, error: process.env.FAKE_APL_FAIL }) + "\n");
    process.exit(1);
  }
  if (process.env.FAKE_APL_GARBAGE) {
    process.stderr.write("connection reset\n");
    process.exit(1);
  }
  if (cmd === "messages edit") ok({ edited: true, id: flag("--id") });
  else if (cmd === "messages revoke") ok({ revoked: true, id: flag("--id") });
  else if (cmd === "send react") ok({ sent: true, to: flag("--to"), id: "R1", target: flag("--id"), reaction: flag("--reaction") });
  else if ((cmd === "send file" || cmd === "send voice") && !process.env.FAKE_APL_NOFILE) ok({ sent: true, to: flag("--to"), id: "SENT1", file: { name: "x" } });
  else ok({ sent: true, to: flag("--to"), id: "SENT1" });
} else if (cmd === "messages show") {
  const m = group.find((x) => x.MsgID === flag("--id") && inChat(x));
  if (!m) {
    process.stdout.write(JSON.stringify({ success: false, error: "not found" }) + "\n");
    process.exit(1);
  }
  ok(m);
} else if (cmd === "messages list") {
  let ms = group.filter(inChat);
  if (flag("--after")) ms = ms.filter((m) => new Date(m.Timestamp) > new Date(flag("--after")));
  if (w.indexOf("--asc") < 0) ms.reverse();
  ok({ messages: ms.slice(0, Number(flag("--limit") || 50)) });
} else {
  process.stderr.write("fake-apl: unexpected " + w.join(" ") + "\n");
  process.exit(2);
}
