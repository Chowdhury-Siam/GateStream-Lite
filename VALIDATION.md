# Release validation

GateStream-Lite v2.0.33 is based on upstream commit
`6ba04417f3309c67328558e3f167c75cd9cb1b21` and retains its original 110 Python tests.

Final release checks:

| Runtime / check | Result |
| --- | --- |
| CPython 3.11.13, Linux x86_64 | 141 tests passed |
| CPython 3.12.14, Linux x86_64 | 141 tests passed |
| Installed pinned requirements, Python 3.11.13 and 3.12.14 | Dependency checks passed |
| Python compilation, POSIX launcher syntax, patch whitespace | Passed |

The 141 tests comprise all 110 original tests plus 31 new regressions.

The regression suite is run using:

```sh
python -m unittest discover -s tests -v
```

The new tests use synthetic credentials, fake Telegram/MongoDB services and real
loopback TCP/HTTP listeners. No production Telegram accounts or databases are
accessed. Coverage includes the commented port/three-token reproduction, explicit
empty overrides, legacy launcher precedence, BOM/CRLF/quoted special characters,
worker auth isolation, handler discovery, quota errors, memory hooks and task
cleanup. The real HTTP tests launch a subprocess for SIGINT and another for
SIGTERM, wait for readiness, verify successful shutdown and confirm the port closes.

This is Linux validation. FreeBSD allocator/RSS paths and EDQUOT/ENOSPC-style
storage failures are simulated. Actual Serv00 permissions, per-account quotas,
live Telegram connections/worker repair, public reverse proxy/Cloudflare behavior
and real-host throughput need deployment verification. No claim of a live Serv00
or FreeBSD test is made.

The Lite repository does not bundle Worker JavaScript or standalone Node/UI tests.
The separately deployed Worker was not changed or live-tested in this update.
