#!/usr/bin/env node
// Spike (read-only): can ONE receive cover every WhatsApp chat of an apl handle,
// cheaply enough to poll every 25 s? Prints counts and timings only — never
// message text, names or JIDs.
//
//   node docs/spikes/2026-11-05-whatsapp-inbox/probe.mjs [label] [polls] [gapSec]
import { spawnSync } from "node:child_process";

const label = process.argv[2] || "biz";
const polls = Number(process.argv[3] || 3);
const gap = Number(process.argv[4] || 25);

function wacli(args) {
  const t = Date.now();
  const r = spawnSync("apl", ["with", "whatsapp:" + label, "--read-only", "--", "wacli", "--json", ...args], { encoding: "utf8", maxBuffer: 64 << 20 });
  const body = JSON.parse(r.stdout || "{}");
  if (!body.success) throw new Error(body.error || r.stderr);
  return { ms: Date.now() - t, data: body.data };
}
const list = (d) => (Array.isArray(d) ? d : d?.messages || d?.chats || []);
const sleep = (s) => new Promise((r) => setTimeout(r, s * 1000));

const chats = wacli(["chats", "list", "--limit", "500"]);
const kinds = {};
for (const c of list(chats.data)) kinds[c.kind || c.jid.split("@")[1]] = (kinds[c.kind || c.jid.split("@")[1]] || 0) + 1;
console.log(`chats list: ${chats.ms} ms, ${list(chats.data).length} chats`, kinds);

// First poll: cursor at the newest message, no history (the channel contract's rule).
const first = wacli(["messages", "list", "--limit", "1"]);
let cursor = list(first.data)[0];
console.log(`first poll: ${first.ms} ms, cursor set (no history emitted)`);
let seen = new Set(cursor ? [cursor.MsgID] : []);

for (let i = 1; i <= polls; i++) {
  await sleep(gap);
  const since = cursor ? new Date(new Date(cursor.Timestamp).getTime() - 1000).toISOString().replace(/\.\d+Z$/, "Z") : "1970-01-01T00:00:00Z";
  const r = wacli(["messages", "list", "--after", since, "--asc", "--limit", "500"]);
  const fresh = list(r.data).filter((m) => !seen.has(m.MsgID));
  for (const m of fresh) { seen.add(m.MsgID); cursor = m; }
  const inbound = fresh.filter((m) => !m.FromMe);
  const byKind = {};
  for (const m of inbound) { const k = m.ChatJID.split("@")[1]; byKind[k] = (byKind[k] || 0) + 1; }
  console.log(`poll ${i}: ${r.ms} ms, ${fresh.length} new (${inbound.length} inbound) across ${new Set(inbound.map((m) => m.ChatJID)).size} chats`, byKind);
}
