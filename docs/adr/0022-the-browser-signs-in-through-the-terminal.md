# ADR 0022 — The browser signs in through the terminal

- **Status:** accepted
- **Date:** 2026-09-28

## Context

ADR 0017 made identity a device grant (RFC 8628). A person signs in with
`wfx login`, and the CLI keeps a bearer in its 0600 contexts file. The
middleware (`internal/api/auth.go`) accepted only `Authorization: Bearer`.

The web UI has no bearer. Its `fetch` calls carry no credential, so on any
server that requires login the UI could not be used. That includes the Docker
compose install, whose requests come in over the docker bridge rather than
loopback. `Identity.tsx` told the person to "run `wfx login`, approve it, then
reload", but that login gave the CLI a token and gave the browser nothing. A
reload still got a 401.

## Decision

**The browser holds the same kind of user token the CLI holds, in an HttpOnly
cookie, and gets it only through a device grant that a signed-in terminal
approves.** There is still no password form and no token field in the UI.

- **The cookie.** It is named `wfx_session` and set with HttpOnly,
  `SameSite=Strict`, `Path=/`, and `Secure` when the request came over https
  (directly, or as `X-Forwarded-Proto`). Its value is a newly minted user
  token, labelled `browser`. The token is stored as its hash like every other
  token, resolved by the same `resolveSubject`, and revoked by the same
  `DELETE /api/tokens/self`, which also clears the cookie.
- **The middleware** reads the cookie only when there is no `Authorization`
  header, so a CLI or worker bearer always wins. A dead cookie gets a 401 and
  is cleared.
- **CSRF.** A cookie-authenticated request that is not GET, HEAD or OPTIONS
  must carry an `Origin` whose host matches `Host` (or `X-Forwarded-Host`). If
  the browser sent no Origin, it must carry `Sec-Fetch-Site: same-origin`.
  Anything else gets a 403. `SameSite=Strict` is the first lock and this is
  the second: SameSite treats a sibling subdomain as the same site.
- **Two ways in, both on the device-grant table:**
  1. **`wfx ui`** is for local installs. The CLI calls `POST /api/auth/link`
     with its bearer. The server creates a device-code row that the caller has
     already approved (`client_id wfx-browser-link`, 60 seconds) and answers
     with `/auth/browser?code=<256-bit random>`. Opening that URL claims the
     row (a DELETE whose row count decides the one winner, so the link works
     once), sets the cookie, and returns `303` to `/`. An expired or used link
     returns 410 with "run `wfx ui` again".
  2. **The sign-in card** is for a browser anywhere. The UI starts a grant with
     `client_id wfx-ui` and shows the user code. The person approves it from a
     signed-in terminal with `wfx login --approve <code>`. That sends the same
     `POST /api/device/verify` the `/device` page sends, with the CLI's bearer.
     The UI polls `POST /api/auth/session`, which is `/api/device/token` with a
     cookie as the answer instead of a bearer value.
- **A browser's grant never becomes a bearer.** `/api/device/token` refuses rows
  whose client is `wfx-ui` or `wfx-browser-link` (`invalid_grant`). So a code
  that has sat in a URL or on a screen can never be turned into a token printed
  to a script.
- A browser that is signed in can also approve a `wfx login` at `/device`
  without pasting a token, because `subjectOf` accepts the cookie under the
  same CSRF rule.

## What does not change

- The loopback solo install still has no auth: no card and no cookie. There,
  `wfx ui` just prints the address.
- There is no long-lived secret in any URL, page or log. The only thing that
  appears in a URL is a code that works once within 60 seconds and can only
  be redeemed for a cookie.
- The terminal stays the place where a person authorizes. The browser only
  receives an identity that a terminal gave it.

## Consequences

- A compose install is usable from the browser with one command:
  `wfx ui --url http://localhost:<port>`. `wfnexus-setup` prints that command
  next to READY.
- Browser sessions are ordinary `user_tokens` rows labelled `browser`, so an
  admin sees them and revokes them like any other token. A cookie lasts 30
  days, and the row lasts until someone revokes it.
