# Changelog

Changes a release's notes must carry (README, Versioning). Copied into the
GitHub release notes when the release is cut.

## Unreleased

- **Sign-in rate limit behind a reverse proxy (#191).** Forwarding headers
  (`X-Forwarded-For`, `X-Real-IP`, `True-Client-IP`) are no longer believed from
  any caller. Only proxies named in `trustedProxies` / `WFX_TRUSTED_PROXIES`
  may say who the client is; the default trusts none, so behind a proxy every
  client shares one limit until it is set. A Kamal / kamal-proxy deploy must
  trust the Docker network kamal-proxy reaches the container on, plus the
  CDN's ranges if one is in front; on Kubernetes, the ingress controller's pod
  CIDR (`server.trustedProxies`). See docs/deployment-auth.md.
- **The images report their release (#190).** `wfx-server --version` in the
  image and `GET /api/health` (`version`, `commit`) answer the release tag.
