#!/usr/bin/env node
// A fake `apl with whatsapp:<label> [--read-only] -- wacli --json …` over a
// JSON store file (FAKE_WA_STORE: {"messages":[wacli store rows]}). Read verbs
// only: the monitor never sends.
const fs = require("node:fs");
const argv = process.argv.slice(2);
const w = argv.indexOf("wacli");
const args = argv.slice(w + 2); // skip "wacli" "--json"
if (!argv.includes("--read-only")) { console.log(JSON.stringify({ success: false, error: "fake apl: write attempted" })); process.exit(0); }
const store = JSON.parse(fs.readFileSync(process.env.FAKE_WA_STORE, "utf8")).messages;
fs.appendFileSync(process.env.FAKE_WA_STORE + ".calls", args.join(" ") + "\n");
const opt = (k) => { const i = args.indexOf(k); return i >= 0 ? args[i + 1] : undefined; };
const out = (data) => console.log(JSON.stringify({ success: true, data }));
let rows = store.slice().sort((a, b) => a.Timestamp.localeCompare(b.Timestamp));
if (args[0] === "messages" && args[1] === "show") {
  const m = rows.find((r) => r.ChatJID === opt("--chat") && r.MsgID === opt("--id"));
  if (!m) { console.log(JSON.stringify({ success: false, error: "not found" })); process.exit(0); }
  out(m);
} else if (args[0] === "messages" && args[1] === "list") {
  if (opt("--chat")) rows = rows.filter((r) => r.ChatJID === opt("--chat"));
  if (opt("--after")) rows = rows.filter((r) => r.Timestamp >= opt("--after"));
  if (!args.includes("--asc")) rows.reverse();
  out({ messages: rows.slice(0, Number(opt("--limit") || 100)) });
} else {
  console.log(JSON.stringify({ success: false, error: "fake apl: unknown " + args.join(" ") }));
}
