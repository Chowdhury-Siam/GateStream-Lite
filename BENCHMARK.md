# GateStream-Lite v3.0.4 download benchmark

These are loopback HTTP measurements with a simulated Telegram transport. The
same 24 MiB + 19 byte file, 28 available fixture bots, one transport lane per
bot, chunk delays, and cold-cache requests were used on each implementation.
The benchmark runs the original Python v2.0.36 HTTP handler, activity tracker,
and stream iterator under Quart and Hypercorn. It runs the original Go v3.0.3
and updated Go v3.0.4 handlers over real HTTP. MongoDB and the Telegram
transport are replaced by local fixtures. It verifies the full body SHA256,
status, Content-Length, and Content-Range for every request.

The delay profile doubles the delay for every fourth 1 MiB Telegram chunk.
The playback workload requests an unaligned byte range using `?inline=1`.
Each cell is the median of three fresh-origin runs. Both Go fixtures use
`MAX_TRANSMISSIONS=16`, and the v3.0.4 fixture uses a 350 MiB Go memory target.

| Base chunk delay | Request | Python 2.0.36 (MiB/s) | Go 3.0.3 (MiB/s) | Go 3.0.4 (MiB/s) | vs. Go 3.0.3 |
| --- | --- | ---: | ---: | ---: | ---: |
| 85 ms | Bulk download | 8.72 | 8.72 | 34.67 | 3.98× |
| 85 ms | Playback seek | 22.84 | 8.92 | 45.26 | 5.07× |
| 250 ms | Bulk download | 2.98 | 2.96 | 11.92 | 4.03× |
| 250 ms | Playback seek | 7.93 | 3.06 | 15.61 | 5.10× |

Peak concurrent simulated Telegram reads were 4 for Python,
1 for Go v3.0.3, and 11 for Go v3.0.4 across all
samples. At 250 ms, the new origin reached the first byte in a median
505 ms for bulk and 255 ms for playback seek.
Peak v3.0.4 fixture process RSS was 60.4 MiB. This includes the fixture
payload and excludes live Telegram client sessions, MTProto crypto and Serv00
overheads. Go heap headroom and the global buffered chunk cap limit read-ahead
in the app. Real 512 MB account usage still needs a deployment measurement.

The local fixture isolates sequential request scheduling. It does not reproduce
the previous Python vs. Go throughput difference observed on Serv00. Live
Telegram latency, hosting limits, channel access, and the Cloudflare proxy are
outside this benchmark. These results establish the local improvement and do
not establish the download speed on Serv00.

## Reproduce

Extract the Python Lite v2.0.36 and Go Lite v3.0.3 source archives to separate
directories. Use Python 3.11 or newer, Go 1.25 or newer, and Quart/Hypercorn in a
temporary Python environment:

```sh
python3 -m venv /tmp/gatestream-bench-env
/tmp/gatestream-bench-env/bin/pip install quart hypercorn
/tmp/gatestream-bench-env/bin/python scripts/bench-download.py \
  --go-baseline /path/to/go-3.0.3/GateStream-Lite \
  --python-baseline /path/to/python-2.0.36/GateStream-Lite
```

The script prints one JSON measurement per response. It makes no Telegram,
MongoDB, or Cloudflare requests.
