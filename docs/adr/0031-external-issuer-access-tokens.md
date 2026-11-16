# 0031: Access tokens from an external issuer

Status: proposed · 2026-11-16 · amends 0030

## Context

ADR 0030 binds a machine credential to one tenant. A person signed in at an external identity service also needs to reach their tenant here. That must work without a second sign-in model on this server and without trusting anything the token asks for beyond who the person is.

## Decision

1. **A generic verifier (`internal/issuer`).** It checks ES256 JWTs against the issuer's JWKS. It is configured at runtime by issuer, audience and JWKS URL: all three or none, and boot refuses a partial set. The verifier names no issuer.
2. **The contract is versioned (`issuer.ContractVersion`, v0.2.0).**
   - The algorithm is decided by the verifier: ES256 only, checked before any key is looked up.
   - The kid must be in the set, and the signature is raw r||s.
   - `iss` and `aud` must match.
   - `exp`, `iat`, `auth_time`, `sub`, `sid` and `tid` are required.
   - Skew is 60 s, and the lifetime is at most 10 minutes.
   - Any role claim is ignored.
   - Golden vectors minted by the issuer's reference implementation (`testdata/`) pin agreement with it. A rule-by-rule suite pins each rule on its own.
3. **JWKS.**
   - Only public P-256 signing keys are kept: no private part, no encryption keys, no other alg or curve, and every point on the curve.
   - Fetches, including the first, happen at most every 30 s, so an unreachable issuer is not asked once per token.
   - An unknown kid refetches within that limit.
   - Redirects are not followed.
   - A failed refetch serves the last set for up to an hour, then fails closed.
4. **The tenant comes from `tid`, through an operator's link** (`tenant_issuer_links`). A token with no `tid`, or an unlinked one, is not a subject.
5. **Revocation compares `auth_time`, never `iat`** (`issuer_revocations`). It is per account, and only for an external tenant linked to the revoking tenant. When a revocation cannot be read, the token is refused.
6. **An account reaches only the tenant reads under `/api/svc/`.** It is refused everywhere else, as a service principal is, and it starts nothing: starting runs stays a service principal's, and is switched off (ADR 0030).

## Consequences

- One issuer per server.
- The customer-facing API (build, create, run) is a later change and stays switched off.
- An equivalent mutant cannot be seen failing: the 64-byte signature-length check, because a longer signature fails verification anyway.
- The fail-closed path on a revocation read error is tested by fault injection: the revocation table is dropped while the link stays readable.
