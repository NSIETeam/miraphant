# Build the Miraphant gateway locally

This service is kept under `services/gateway/` and has its own build entry point. Building it does not publish or deploy anything.

## Tool versions

- Upstream baseline: OneAPI `v0.6.10` at `3915ce9814b8261a1ab13ed93adec58b463cd75c`.
- Go: `1.25.1` (the repository `go.sum` pins Go module content).
- Node.js: `24.20.0`.
- npm: `11.19.0`.

The upstream tag had no npm lockfiles. The three lockfiles were generated from the existing `package.json` declarations without changing those declarations; future frontend builds use `npm ci` so they use the checked-in dependency trees.

## Build

From the repository root, set `GO_BIN` to an installed Go 1.25.1 executable if `go` on `PATH` is not that version, then run:

```sh
GO_BIN=/path/to/go-1.25.1 services/gateway/scripts/build-local.sh
```

The script runs `npm ci` and builds the default, berry, and air themes. The generated frontend files are staged under `services/gateway/web/build/` (ignored by Git) so the Go embed directive can include them. The executable and `build-info.txt` go to `../miraphant-gateway-build/` next to the repository by default. Set `MIRAPHANT_GATEWAY_BUILD_DIR` to choose another output directory. It does not read a production `.env` or deploy the result.

For a build on the task host used for the initial import:

```sh
GO_BIN=/Users/king/Documents/Codex/2026-09-27/du/work/toolchains/golang.org/toolchain@v0.0.1-go1.25.1.darwin-arm64/bin/go services/gateway/scripts/build-local.sh
```

The local executable is for the build host's architecture (the initial task host is macOS/arm64); that artifact is not a production Linux/amd64 binary. The build-check workflow runs the same entry point on Linux/amd64. Neither build publishes or deploys the result.

## Recover interrupted point requests (SQLite)

For the current single-instance SQLite deployment, stop the gateway before running offline recovery. The command marks every `held` request as `pending`, writes an auditable batch and per-request record, and keeps all frozen points reserved. It never releases a balance or estimates a charge; an operator must review provider evidence and resolve each pending hold afterward.

```sh
one-api --recover-point-holds \
  --points-recovery-batch ops-2026-09-27-01 \
  --points-recovery-reason "gateway stopped during provider response"
```

The command uses the database configured for the gateway and exits without starting HTTP service. Reusing the same batch key and reason is a no-op and does not include later requests; use a new batch key for a later recovery. A different reason for an existing batch is rejected. This recovery path currently supports a single-instance SQLite database only. Multi-instance coordination and MySQL/PostgreSQL recovery have not been implemented or verified.
