# Token conformance fixtures

`token-vectors.json` holds access tokens minted by the token issuer's reference
implementation with a TEST key derived from a public seed. The key set has no
private part, and the ids in the tokens are fake.

- Contract: v0.2.0 (`issuer.ContractVersion`)
- Source: the issuer's public golden set, commit a6692cd
- Values: issuer `https://issuer.example.test`, audience `product-a`, verify_at
  2026-11-16T12:00:00Z, max clock skew 60 s

The `contract` field was added here when the file was copied; the tokens
are unchanged. `TestGoldenVectors` refuses a fixture whose contract is not
`issuer.ContractVersion`.

`TestGoldenVectors` must give every vector the verdict recorded in it. When
the contract changes, regenerate this file from the reference implementation
and update the conformance suite in the same change. Never edit a token by
hand: each one is signed.
