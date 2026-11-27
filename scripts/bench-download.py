#!/usr/bin/env python3
"""Loopback HTTP benchmark with identical delayed, one-lane Telegram fixtures.

Requires Go, a local v3.0.3 Go checkout and a v2.0.36 Python checkout.
Install quart and hypercorn in a temporary Python environment to run it.
"""
import argparse
import ast
import asyncio
from contextlib import aclosing, contextmanager, suppress
import hashlib
import http.client
import json
import logging
import math
import os
from pathlib import Path
import re
import runpy
import socket
import subprocess
import sys
import tempfile
import time
from types import SimpleNamespace

CHUNK = 1024 * 1024


def load_functions(path, names, namespace):
    tree = ast.parse(path.read_text(), filename=str(path))
    funcs = [node for node in tree.body if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name in names]
    if len(funcs) != len(names):
        raise ValueError(f"Expected functions missing in {path}")
    for node in funcs:
        node.decorator_list = []
    exec(compile(ast.Module(body=funcs, type_ignores=[]), str(path), "exec"), namespace)


def python_origin(project, address, delay, size):
    from quart import Quart, Response, abort, request
    from hypercorn.asyncio import serve
    from hypercorn.config import Config

    app = Quart(__name__)
    chunks = [bytes([1 + i % 251]) * min(CHUNK, size - i * CHUNK) for i in range(math.ceil(size / CHUNK))]
    inflight = peak = next_worker = 0

    def make_worker(i):
        lane = asyncio.Semaphore(1)

        async def stream_media(message, offset=0, limit=0):
            nonlocal inflight, peak
            for index in range(offset, min(len(chunks), offset + limit)):
                async with lane:
                    inflight += 1
                    peak = max(peak, inflight)
                    try:
                        await asyncio.sleep(delay * (2 if index % 4 == 0 else 1))
                        chunk = memoryview(chunks[index]).tobytes()
                    finally:
                        inflight -= 1
                yield chunk

        return SimpleNamespace(name=f"worker_{i}", stream_media=stream_media)

    workers = [make_worker(i) for i in range(28)]

    def get_worker_client():
        nonlocal next_worker
        worker = workers[next_worker % len(workers)]
        next_worker += 1
        return worker

    async def get_message(*args, **kwargs):
        return SimpleNamespace(chat=SimpleNamespace(id=-1000000000001), id=1)

    async def get_file(code):
        return dict(channel_id=-1000000000001, message_id=1, file_name="fixture.mkv",
                    mime_type="video/x-matroska", file_size=size)

    async def cache_miss(*args, **kwargs):
        return None

    class FloodWait(Exception):
        pass

    activity = runpy.run_path(str(project / "bot/modules/activity.py"))["TransferActivity"]()
    ns = dict(asyncio=asyncio, hashlib=hashlib, aclosing=aclosing, request=request, Response=Response,
              abort=abort, ceil=math.ceil, re_match=re.match, fullmatch=re.fullmatch, logger=logging.getLogger("fixture"),
              FloodWait=FloodWait, MEDIA_READ_TIMEOUT=18.0, guess_type=__import__("mimetypes").guess_type,
              get_message=get_message, get_file=get_file, activity=activity, get_worker_client=get_worker_client,
              worker_clients=workers, TelegramBot=workers[0], get_cached_chunk=cache_miss,
              is_chunk_cached=lambda *args: True, is_head_or_tail=lambda *args: False,
              mark_worker_cooldown=lambda *args: None)
    load_functions(project / "bot/modules/telegram.py", {"iter_media_chunks", "get_media_chunk", "resolve_file_mime"}, ns)
    load_functions(project / "bot/server/main.py", {"transmit_file", "download_client"}, ns)
    app.add_url_rule("/dl/<string:file_code>", view_func=ns["transmit_file"])

    @app.get("/health")
    async def health():
        return "OK"

    @app.get("/benchmark-peak")
    async def read_peak():
        return str(peak)

    config = Config()
    config.bind = [address]
    config.accesslog = None
    asyncio.run(serve(app, config))


def request(address, path, headers=None):
    link = http.client.HTTPConnection(address, timeout=60)
    start = time.perf_counter()
    link.request("GET", path, headers=headers or {})
    response = link.getresponse()
    first = response.read(1)
    ttfb = time.perf_counter() - start
    digest = hashlib.sha256(first)
    size = len(first)
    while packet := response.read(256 * 1024):
        digest.update(packet)
        size += len(packet)
    elapsed = time.perf_counter() - start
    status, length, span = response.status, response.getheader("Content-Length"), response.getheader("Content-Range")
    link.close()
    return status, length, span, size, digest.hexdigest(), elapsed, ttfb


@contextmanager
def run_origin(command, address, env):
    with tempfile.TemporaryFile(mode="w+") as log:
        proc = subprocess.Popen(command, env=env, stdout=log, stderr=log)
        try:
            for _ in range(150):
                if proc.poll() is not None:
                    log.seek(0)
                    raise RuntimeError(log.read())
                try:
                    if request(address, "/health")[0] == 200:
                        break
                except (ConnectionError, OSError):
                    time.sleep(.04)
            else:
                raise RuntimeError(f"Origin didn't start at {address}")
            yield proc
        finally:
            with suppress(ProcessLookupError):
                proc.terminate()
            proc.wait(timeout=10)


def run_bench(args):
    current = Path(__file__).resolve().parent.parent
    baseline = args.go_baseline.resolve()
    python_base = args.python_baseline.resolve()
    with tempfile.TemporaryDirectory(prefix="gatestream-bench-") as temp:
        temp = Path(temp)
        # Copy the baseline to avoid changing an input checkout.
        import shutil
        old = temp / "baseline"
        shutil.copytree(baseline, old, ignore=shutil.ignore_patterns(".git", "bin"))
        fixture = (current / "media_benchmark_test.go").read_text()
        fixture = re.sub(r"^\s*bufferSlots:.*\n", "\n", fixture, flags=re.MULTILINE)
        (old / "media_benchmark_test.go").write_text(fixture)
        helpers = (current / "media_test.go").read_text().split("type testMediaBackend", 1)[1].split("func TestParallelRangeDeliveryAndWorkerReuse", 1)[0]
        (old / "benchmark_backend_test.go").write_text(
            'package main\nimport ("bytes";"context";"errors";"sync/atomic";"time";"github.com/gotd/td/bin";"github.com/gotd/td/tg")\n'
            "const mediaChunkSize = 1 << 20\n" + "type testMediaBackend" + helpers)
        go = os.environ.get("GATESTREAM_BENCH_GO", "go")
        env = dict(os.environ, GOMAXPROCS="2", GOGC="20", CGO_ENABLED="0")
        for label, project in (("go-3.0.3", old), ("go-current", current)):
            subprocess.run([go, "test", "-trimpath", "-c", "-o", str(temp / label), "."], cwd=project, env=env, check=True)
        size = args.mib * CHUNK + 19
        payload = b"".join(bytes([1 + i % 251]) * min(CHUNK, size - i*CHUNK) for i in range(math.ceil(size / CHUNK)))
        workloads = [("full", "/dl/file", 0, size-1), ("seek", "/dl/file?inline=1", 2*CHUNK+123, size-2*CHUNK-18)]
        for delay in args.delay_ms:
            for repeat in range(1, args.repeats + 1):
                # Use a new origin per workload for cold, comparable requests.
                for label in ("python-2.0.36", "go-3.0.3", "go-current"):
                    for name, path, first, last in workloads:
                        with socket.socket() as sock:
                            sock.bind(("127.0.0.1", 0))
                            address = f"127.0.0.1:{sock.getsockname()[1]}"
                        command = [str(temp / label), "-test.run=^TestMediaBenchmarkOrigin$"]
                        if label.startswith("python"):
                            command = [sys.executable, __file__, "--python-origin", str(python_base), address, str(delay), str(size)]
                        benv = dict(env, TZ="UTC", GOMEMLIMIT="350MiB", GATESTREAM_BENCH_ADDRESS=address,
                                    GATESTREAM_BENCH_DELAY=f"{delay}ms", GATESTREAM_BENCH_SIZE=str(size))
                        with run_origin(command, address, benv) as proc:
                            headers = {} if name == "full" else {"Range": f"bytes={first}-{last}"}
                            status, length, span, got, digest, sec, ttfb = request(address, path, headers)
                            want = last - first + 1
                            if (status, length, span, got, digest) != (
                                200 if name == "full" else 206, str(want),
                                None if name == "full" else f"bytes {first}-{last}/{size}", want,
                                hashlib.sha256(memoryview(payload)[first:last+1]).hexdigest(),
                            ):
                                raise RuntimeError(f"{label} returned wrong HTTP headers or bytes on {name}")
                            peak_link = http.client.HTTPConnection(address, timeout=5)
                            peak_link.request("GET", "/benchmark-peak")
                            peak = int(peak_link.getresponse().read())
                            peak_link.close()
                            proc_status = Path(f"/proc/{proc.pid}/status")
                            rss = None
                            if proc_status.exists():
                                hwm = re.search(r"^VmHWM:\s+(\d+)", proc_status.read_text(), re.M)
                                rss = int(hwm[1])/1024 if hwm else None
                            print(json.dumps(dict(origin=label, case=name, delay_ms=delay, repeat=repeat,
                                mib_per_sec=round(got/CHUNK/sec, 3), first_byte_ms=round(1000*ttfb, 2),
                                concurrent_mtp_reads=peak, peak_rss_mib=rss)), flush=True)


if __name__ == "__main__":
    if len(sys.argv) > 1 and sys.argv[1] == "--python-origin":
        _, _, project, address, delay, size = sys.argv
        python_origin(Path(project), address, float(delay)/1000, int(size))
    else:
        parser = argparse.ArgumentParser(description=__doc__)
        parser.add_argument("--go-baseline", type=Path, required=True)
        parser.add_argument("--python-baseline", type=Path, required=True)
        parser.add_argument("--delay-ms", type=int, nargs="+", default=[85, 250])
        parser.add_argument("--mib", type=int, default=24)
        parser.add_argument("--repeats", type=int, default=3)
        run_bench(parser.parse_args())
