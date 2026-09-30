# Swap envelope audit and simplification plan

## Decision and goal

Keep both verifiers. Simplify their representation and orchestration; preserve the
spending policy. These files validate unsigned, externally supplied transactions
that the wallet will sign. Removing authorization checks changes financial safety.

Goal: make the XOXNO and LI.FI verification flows readable in one pass, reject the
confirmed malformed transaction cases before signing, and keep backend/mobile
acceptance decisions aligned. Do not merge the providers into a generic validator.

This document completes the audit/plan stage. No envelope refactor is implemented
with it. The separately committed LI.FI endpoint configuration is independent.

## Scope and actual flow

Reviewed the complete 221-line `envelope.go`, 261-line `lifi_envelope.go`, route
payload parser, quote callers, mobile verification mirrors, transaction preparation,
existing mutations, captured unsigned fixtures, and local contract ABI/source.
Two independent reviewers checked the providers; the primary reviewer reran their
acceptance-gap reproductions.

| Stage | XOXNO | LI.FI |
| --- | --- | --- |
| Backend entry | `xoxnoSource.attachTransaction` | `lifiSource.Quote` |
| Header | Shared unsigned v1, sender, fee, resource-data and operation checks | Same shared checks; additional resource-fee check |
| Trade | Pinned `execute_strategy(sender, input, payload)`; route endpoints/minimum | Pinned wrapper `swap`; wrapper and Soroswap arguments, fee and deadline |
| Authorization | Matching root; only direct source-token transfers into the router; cumulative spend at most input | Fee transfer, matching Soroswap call, supported Aquarius routes and transfers; fee plus venue spend equals gross input |
| Sequence/expiry | Backend stamps fresh sequence and transaction expiry | Backend validates and preserves the simulated sequence and expiry |
| Wallet | `prepareAggregatorSwap` calls `verifyAggregatorSwap` before retaining XDR for signing | Same entry; branches into `checkLifiSwap` |

Backend filtering and mobile verification have separate trust boundaries. Backend
checks protect quote selection; mobile checks protect signing an externally supplied
envelope. Both remain necessary. Authorization trees contain sender consent, not
every contract-funded execution call.

## Confirmed findings

All six cases below are accepted by the current backend verifier in local mutation
tests. They establish acceptance gaps, not successful execution or fund loss.
No fund-loss bypass was confirmed in this audit.

| Finding | Evidence/location | Impact and action |
| --- | --- | --- |
| XOXNO resource fee lacks bounds | `envelope.go:97–117`; both real XOXNO fixtures accept `ResourceFee=-1` and `ResourceFee=Tx.Fee+1` | Invalid fee accounting can reach mobile, which subtracts resource fee from total fee. Move LI.FI's existing bounds check into shared validation and the common mobile header. |
| XOXNO payload duplicate fields overwrite | `payload.go:68–92`; four serialized fields with duplicate `amounts` pass | Host map decoding requires sorted, unique keys. Reuse the existing strict map parser. Mobile additionally accepts extra fields; align its policy. |
| LI.FI `tracking_id` type unchecked | `lifi_envelope.go:113`; replacing its value with `scvVoid` and matching the auth root passes | Captured deployed `SwapStep` ABI requires `i128`. Validate the type, without constraining its business value. |
| LI.FI intermediate path types unchecked | `lifi_envelope.go:221–248`; an `i128(0)` intermediate token mirrored into authorized hops passes | Soroswap expects `Vec<Address>`. Check each path element's type. |
| LI.FI Aquarius minimum can differ from actual call | `lifi_envelope.go:92–95`; authorized minimum `1` passes | Reviewed Soroswap adapter passes `0` per venue and checks aggregate output afterwards. Require that ABI value while preserving the positive outer minimum. |
| LI.FI split amounts need only sum correctly | `lifi_envelope.go:91–105`; equal-weight routes with amounts `1` and `net-1` pass | Reviewed Soroswap calculates weighted amounts, with rounding remainder assigned to the last route. Bind each authorized amount to that calculation after fixture/ABI validation. |

The contract/host rejects the malformed types/maps; mismatched authorization
arguments fail when the executed call requests authorization. Those rejection
claims follow local source/ABI inspection, not a fresh on-chain simulation.
Mobile has the corresponding omissions by source inspection; the new mutation
corpus must exercise them independently in TypeScript.

Additional map ordering gaps belong to the same parser fix. Current LI.FI maps
reject duplicate/missing/extra names but do not check canonical order. All existing
strict-parser call sites already supply field names in sorted order.

## Checks to preserve

- Unsigned v1 envelope, expected source account, one contract invocation, bounded
  total fee, valid Soroban resource fee, and current memo/operation-source policy.
- Pinned contracts and functions, expected sender/recipient, token endpoints,
  exact input and positive minimum at least the quoted minimum.
- Source-account credentials only; auth root equals the actual approved call.
- XOXNO: only direct source-token transfers from sender into router, nonnegative
  amounts and one cumulative spend ceiling across every auth entry.
- LI.FI: exact fee/BPS/recipient, net-input Soroswap arguments, supported Aquarius
  ancestors and leaf transfers, complete gross-input accounting.
- LI.FI's directed output token and pool binding. Aquarius sorts each token pair;
  pair order alone does not express swap direction.
- Distinct `i128` and `u128` decoding. Aquarius uses unsigned amounts; token
  transfers and outer trade arguments use signed amounts.
- XOXNO stamping versus LI.FI preservation of simulation-bound sequence/expiry.

Strict fee count/order, single LI.FI auth entry, Aquarius-only routes, zero XOXNO
referral and pinned deployment addresses express the supported product policy.
Keep them in this refactor; relaxing them needs a separate compatibility change.

## Safe simplifications

1. Replace LI.FI's manual net-input `ScVal` construction (`:67–70`) with argument
   count, existing `scValI128` comparison at index 2, and equality checks for the
   remaining arguments. No integer-to-XDR helper is needed.
2. Remove the impossible negative check after successful `u128` decoding, retaining
   decoding and the expected zero-minimum check. Apply the same change in mobile.
3. In LI.FI, keep one route-nonempty check plus route/auth count equality; remove
   the second nonempty check. Once every amount is positive, final exact-sum
   validation also implies the intermediate upper bound. Remove that repeated
   bound only after the positive-amount and final-total regressions pass. Retain
   unconditional accumulation: `spent.Add(spent, amount)` currently lives inside
   the condition. XOXNO has no final exact-sum check; retain its cumulative ceiling.
4. Reuse `strictScMap` for XOXNO payloads. Match serialized keys to the sorted
   expected names by index: one pass rejects duplicates, extra/missing names and
   unordered maps. Retain map/type/nil checks and require serialized entry count
   to equal expected count before indexing. Use errors that describe the struct
   instead of naming LI.FI.
5. Keep `prepareEnvelope` as orchestration. Divide LI.FI into existing header/trade
   validation, Soroswap authorization binding, and Aquarius authorization checks.
   Extract only cohesive blocks with independent invariants; keep financial
   arithmetic beside the checks that use it.

Keep `reflect.DeepEqual` for exact decoded invocation binding. Replacing it with
field-by-field checks increases omission risk. Avoid a recursive tree walker that
merely counts transfer leaves: unknown `approve`, `burn` or administrative calls
can require authorization without those leaves.

## Ordered implementation plan

1. **Preserve evidence.** Add the two real XOXNO fixtures already in mobile to
   backend tests; retain both LI.FI directions. Convert the six acceptance probes
   into rejection regressions as their fixes land. Add duplicate/extra/unordered
   maps and multi-entry cumulative-spend cases. Match identical unsigned fixtures
   and expected trade metadata in Go and TypeScript, without a runtime dependency
   between repositories.
2. **Fix shared parsing and fee accounting.** Move resource-fee bounds into the
   existing shared header check. Consolidate strict map parsing and canonical
   ordering. Mirror both in mobile. This is a small correctness change before
   structural refactoring.
3. **Bind the supported LI.FI ABI.** Validate tracking ID type and all path address
   types; require the actual Aquarius minimum. Check exact weighted splits with
   last-route remainder and checked `u32` parts totals. Validate the narrow
   calculation against the reviewed source algorithm, preserving checked signed
   `i128` multiply/add/subtract as well as checked `u32` parts totals. ABI establishes
   types, not arithmetic. Add synthetic multi-hop/split/rounding/overflow cases;
   require source-to-deployed-WASM matching or captured executable split evidence
   before claiming deployment compatibility. Avoid mirroring the entire router.
4. **Apply the safe simplifications.** Remove redundant checks, stop rebuilding
   the net-input argument, and extract the cohesive LI.FI auth stages. Mirror
   policy changes in mobile in the same review batch. Add contextual errors such
   as `LI.FI Aquarius route 1: pool mismatch` using existing error wrapping.
5. **Verify and independently review.** Run backend swap tests/race tests, scoped
   vet/lint, both mobile verifier suites, TypeScript and changed-file lint. Have a
   context-free reviewer inspect the final financial diff and fixture decisions.
   Commit/push to the existing draft branches; preserve local native iOS edits.

Completion requires: every confirmed malformed case rejected; existing valid
fixtures still accepted; no new unknown authorization accepted; LI.FI XDR preserved
byte-for-byte; XOXNO changes limited to sequence/expiry; backend/mobile policy
decisions agree; the main flows can be read without decoding ad hoc XDR arithmetic.
Line count is not a reason to remove a financial invariant.

## Verification and limits

- Focused backend tests (`cmd/serve`, `internal/services/swap`, `internal/api`),
  scoped vet and golangci-lint pass. Existing mobile verifier suites pass.
- Primary reviewer independently reran both temporary Go overlays. XOXNO accepts
  both captured envelopes, the invalid resource fees, and duplicate field probe.
  LI.FI accepts all four ABI mutation probes. These probes assert current
  acceptance; they are not tests of successful contract execution.
- Both LI.FI captures contain one distribution and one hop. The reverse capture
  covers sorted pair/directed output behavior, not multi-hop or split routing.
- XOXNO's existing synthetic fixtures often omit transfer children and contain no
  route operations. They test verifier boundaries, not executability.
- Reviewed XOXNO source validates the program before pulling input, checks the
  post-fee output floor and pays the sender. Captured LI.FI ABI/source supports the
  reviewed shape. Current deployed code identity was not reverified. Pinned
  addresses do not freeze upgradeable contract behavior; envelope checks cannot
  replace contract review.
- No transaction was signed/broadcast and no new remote LI.FI diagnostics were
  performed. This plan makes no claim about deployed provider availability or
  measurable UI latency improvement.

Local reproduction artifacts, retained outside the repository:

```sh
go test -overlay=/private/tmp/envelope-audit-u9l68e4j/overlay.json ./internal/services/swap -run '^TestAuditLocal' -count=1 -v
go test -overlay=/private/tmp/lifi-audit-overlay.json ./internal/services/swap -run '^TestAuditLifiAcceptedInvalidABI$' -count=1 -v
```

Reviewed contract references: local XOXNO `execute/mod.rs` and Soroban host
`MeteredOrdMap::from_map`; captured `/private/tmp/lifi-router-deployed-spec.json`
(`tracking_id: i128`), `/private/tmp/soroswap-models.txt` (`path: Vec<Address>`),
`/private/tmp/soroswap-lib.txt` (weighted inputs, remainder, venue minimum), and
`/private/tmp/soroswap-aqua.txt` (sorted token pairs and directed output).
