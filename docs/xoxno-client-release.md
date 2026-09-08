# XOXNO client release prerequisite

Local validation intentionally uses `replace github.com/stellar/wallet-backend => ../wallet-backend`. The current pinned version, `v0.0.0-20260729203253-5182ba495ddf`, predates these corrections. A successful local build with the replacement does not establish standalone image readiness.

Publication and deployment are outside the local hardening goal. Once separately authorized, execute this sequence:

1. Publish the reviewed Wallet Backend changes containing ledger-aware snapshots and the `live_ledger` column. Record the immutable published commit and its module version. The corrected indexer must also run these changes and the migration to provide their data guarantees; updating a client alone does not upgrade a running indexer.
2. In Freighter Backend v2, pin `github.com/stellar/wallet-backend` to that exact immutable version. Do not use `latest` or a branch name. Set `XOXNO_WB_VERSION` to the published tag or Go pseudo-version, then run:

   ```sh
   go mod edit -require=github.com/stellar/wallet-backend@"$XOXNO_WB_VERSION"
   go mod edit -dropreplace=github.com/stellar/wallet-backend
   go mod tidy
   go mod verify
   go list -m -json github.com/stellar/wallet-backend
   ```

   Check that `Version` is the recorded version and `Replace` is absent. Review the exact `go.mod` and `go.sum` changes.

3. From a clean standalone API checkout containing that reviewed module update, with no sibling Wallet Backend checkout or Go workspace replacement, run:

   ```sh
   GOWORK=off go test ./internal/services -run 'Xoxno|Earn|AccountAggregate|AccountsPositions|ToBaseUnits|ApplyWithdrawable|Depositable'
   GOWORK=off go build ./...
   docker build --pull --no-cache -f deployments/Dockerfile -t freighter-backend:xoxno-verified .
   ```

   The Docker context is the API repository only. Record the checked-out API commit, Wallet Backend module version, successful build log and resulting image ID. A failure is a release blocker; do not restore a sibling replacement to claim this check passed.

4. Repeat the API/indexer integration and freshness checks against the intended environment before deployment. Legacy rows without `live_ledger` remain unavailable for live pricing until the snapshot job refreshes them; event indexes remain the fallback. Observe successful snapshot refreshes before enabling withdrawals that need debt valuation.

No publication, replacement removal, image publication or deployment was performed by the local hardening work.
