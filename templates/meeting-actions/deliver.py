#!/usr/bin/env python3
"""deliver.py — the ONLY outward act in the leadership templates, and it runs
only in a step marked `requires_approval: true`, after a person approved it.

Channels (apl-skill mail.md recipes):
  gmail-draft  MAIL-W-13  create a Gmail draft (nothing is sent)
  gmail-send   MAIL-W-11  send through Gmail
  ms-draft     MAIL-W-6   create an Outlook draft (nothing is sent)
  ms-send      MAIL-W-1   send through Outlook
  none                    do nothing

DRY_RUN=true prints what it would do and touches nothing — a second lock
behind the approval gate, so a dry run can never write to a mailbox.

Usage: deliver.py --md brief.md --channel gmail-draft --handle google:work
                  --to me@example.com [--cc a,b] --subject "Daily brief"
"""
import argparse
import base64
import email.message
import json
import os
import subprocess
import sys

p = argparse.ArgumentParser()
p.add_argument("--md", required=True)
p.add_argument("--channel", default="none")
p.add_argument("--handle", default="")
p.add_argument("--to", default="")
p.add_argument("--cc", default="")
p.add_argument("--subject", default="Brief")
a = p.parse_args()

dry = os.environ.get("DRY_RUN", "").lower() in ("1", "true", "yes")
body = open(a.md).read()
if a.channel == "none":
    print("delivery: none configured — the brief stays a local draft")
    sys.exit(0)
if not a.handle or not a.to:
    print("delivery needs --handle and --to")
    sys.exit(2)
if dry:
    print(f"DRY RUN — would {a.channel} via {a.handle} to {a.to} "
          f"(cc {a.cc or '-'}) subject {a.subject!r}, {len(body)} chars. Nothing written.")
    sys.exit(0)

GMAIL = "https://gmail.googleapis.com/gmail/v1/users/me"
GRAPH = "https://graph.microsoft.com/v1.0"
if a.channel.startswith("gmail"):
    m = email.message.EmailMessage()
    m["To"], m["Subject"] = a.to, a.subject
    if a.cc:
        m["Cc"] = a.cc
    m.set_content(body)
    raw = base64.urlsafe_b64encode(m.as_bytes()).decode()
    url, payload = ((f"{GMAIL}/drafts", {"message": {"raw": raw}}) if a.channel == "gmail-draft"
                    else (f"{GMAIL}/messages/send", {"raw": raw}))
elif a.channel.startswith("ms"):
    msg = {"subject": a.subject, "body": {"contentType": "Text", "content": body},
           "toRecipients": [{"emailAddress": {"address": x.strip()}} for x in a.to.split(",") if x.strip()],
           "ccRecipients": [{"emailAddress": {"address": x.strip()}} for x in a.cc.split(",") if x.strip()]}
    url, payload = ((f"{GRAPH}/me/messages", msg) if a.channel == "ms-draft"
                    else (f"{GRAPH}/me/sendMail", {"message": msg, "saveToSentItems": True}))
else:
    print(f"unknown channel {a.channel}")
    sys.exit(2)

r = subprocess.run(["apl", "call", a.handle, "POST", url, "--body-file", "-"],
                   input=json.dumps(payload), capture_output=True, text=True, timeout=120)
if r.returncode != 0:
    print(f"delivery failed ({a.channel}): {(r.stderr or r.stdout)[:300]}")
    sys.exit(1)
print(f"delivered: {a.channel} via {a.handle} to {a.to}")
