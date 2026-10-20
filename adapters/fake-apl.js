#!/usr/bin/env node
// fake-apl — a stand-in for `apl with whatsapp:<label> -- wacli --json …`, used
// by apl-whatsapp.test.js. It records every argv to $FAKE_APL_LOG and answers
// from $FAKE_APL_GROUP (a JSON array of wacli messages, oldest first) in
// wacli's own {"success","data"} shape. $FAKE_APL_FAIL makes sends fail.
"use strict";
const fs = require("node:fs");
const argv = process.argv.slice(2);
fs.appendFileSync(process.env.FAKE_APL_LOG, JSON.stringify(argv) + "\n");
const w = argv.slice(argv.indexOf("--") + 1); // wacli --json <cmd…>
const flag = (n) => (w.indexOf(n) >= 0 ? w[w.indexOf(n) + 1] : undefined);
const group = JSON.parse(fs.readFileSync(process.env.FAKE_APL_GROUP, "utf8"));
const ok = (data) => process.stdout.write(JSON.stringify({ success: true, data }) + "\n");
const cmd = w.slice(2, 4).join(" ");
if (cmd === "send text" || cmd === "send file") {
  if (process.env.FAKE_APL_FAIL) {
    process.stdout.write(JSON.stringify({ success: false, error: process.env.FAKE_APL_FAIL }) + "\n");
    process.exit(1);
  }
  ok({ sent: true, to: flag("--to"), id: "SENT1" });
} else if (cmd === "messages show") {
  if (process.env.FAKE_APL_SHOW_FAIL) {
    process.stdout.write(JSON.stringify({ success: false, error: process.env.FAKE_APL_SHOW_FAIL }) + "\n");
    process.exit(1);
  }
  const m = group.find((x) => x.MsgID === flag("--id"));
  if (!m) {
    // wacli's own not-found: sql.ErrNoRows.
    process.stdout.write(JSON.stringify({ success: false, error: "sql: no rows in result set" }) + "\n");
    process.exit(1);
  }
  ok(m);
} else if (cmd === "messages list") {
  let ms = group.slice();
  if (flag("--after")) ms = ms.filter((m) => new Date(m.Timestamp) > new Date(flag("--after")));
  if (w.indexOf("--asc") < 0) ms.reverse();
  ok({ messages: ms.slice(0, Number(flag("--limit") || 50)) });
} else {
  process.stderr.write("fake-apl: unexpected " + w.join(" ") + "\n");
  process.exit(2);
}
