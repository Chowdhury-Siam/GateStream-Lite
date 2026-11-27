# GateStream-Lite v3.0.4 validation

- All 41 Go regression tests passed on Linux AMD64. The benchmark fixture
  is skipped during ordinary tests and was run separately. The three new
  parallel-read regression tests also passed 20 repeat runs.
- Regression coverage includes out-of-order worker completion, byte-exact
  playback ranges and legacy links, account-specific reference reuse,
  shared buffer limits, cancellation, and per-DC connection reuse.
- Go vet, deployment launcher checks, and all nine Cloudflare proxy tests
  passed.
- Both Linux AMD64 and FreeBSD AMD64 release builds passed SHA256
  verification. The Linux foreground and Cron launchers reported version
  3.0.4. The FreeBSD test binary cross-compiled.
- See the measured rates and methodology in `BENCHMARK.md` and the raw
  benchmark results in `benchmark-results.jsonl`. The fixture verifies
  byte equality and HTTP Range responses.

FreeBSD execution, live Telegram throughput and real Serv00 account memory
usage have not been measured for this update.

## Previous validation for v3.0.3

- All 38 Go tests passed on Linux AMD64. The new tests cover HTTP readiness
  during auxiliary startup, memory and concurrency limits, retry, reconnect,
  and cancellation. Both worker lifecycle tests also passed 25 repeat runs.
- Go vet and deployment launcher checks passed.
- The Linux AMD64 and FreeBSD AMD64 binaries built with CGO disabled. The
  FreeBSD test binary cross-compiled.
- Both release SHA256 hashes verified. The Linux foreground and Cron launchers
  reported version 3.0.3.

FreeBSD runtime behavior, live Telegram authentication, and process RSS under
Serv00 account limits require a deployed check.

## Previous validation for v3.0.2

- Reproduced the v3.0.1 failure by running the compiled Linux test binary through
  a relative path: both lock test groups failed with `no such file or directory`.
- All 36 top-level Go tests passed in the updated GitHub package when its
  compiled regression binary was invoked through a relative path.
- The complete Go suite also passed from the Lite package.
- `go vet ./...` passed for the shared Go sources.
- All 9 Cloudflare Worker tests passed.
- Refreshed Linux AMD64 and FreeBSD AMD64 release binaries compiled with CGO
  disabled. The full FreeBSD AMD64 regression binary also cross-compiled.
- Release checksum verification passed for both packages, and both Linux
  launchers reported version 3.0.2, including a sparse Cron-style environment.
- Shell syntax and deployment-launcher regression checks passed in both packages.
- The FreeBSD runner passed success and injected-failure fixtures, including
  project paths containing spaces and original exit statuses 23 and 37.
- A CI handoff fixture confirmed that the VM log and exit-status file remain
  available before the subsequent runner step reports the original failure.

The FreeBSD CI runner now prints each stage and command, retains its failure
status, and copies the VM log back for upload even when regression tests fail.
The Linux CI job also executes the compiled lock tests using a relative path.

The application restart already uses `os.Executable()`; this update corrects
subprocess and exec paths in the regression tests. File routes, MongoDB names,
settings and session formats are preserved.

FreeBSD execution is configured in GitHub Actions but was not run in this local
Linux environment. Live Telegram authentication, MongoDB persistence and Serv00
resource use still require deployment verification.
