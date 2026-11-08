# load-context

A building block that other workflows **embed** (`uses: [{use: load-context, as: ctx}]`),
not something you run by itself (`on: workflow_call`).

It writes `context.json` into the caller's workspace: chat, chat name and
type, sender, message id, kind, text, replyTo, media, fromOwner, and the
watch's name, instructions, skills and responsePolicy. The caller's
agent reads one file. The chat text stays data and is never put into a command.

Used by: `frontdesk`.
