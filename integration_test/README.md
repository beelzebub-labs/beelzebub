# Integration Tests

Run the durable event container check from the repository root:

```bash
python3 integration_test/durable_events.py
```

It rebuilds the root Dockerfile locally, uses a synthetic Platform on a disposable
internal network, and tests HTTP 500/502/503/504 retries, full EventDTO
acknowledgements above 64 KiB, a lost acknowledgement,
and SIGKILL followed by container removal/recreation with a retained named volume.
It checks UUIDv7 generation, retries staying on the oldest pending event, and
chronological key order when the recreated sensor drains that volume.
The sensor runs as UID/GID 65532. No host ports or existing containers are used;
only test-created containers, volume, network, and uniquely tagged image are removed.
Configuration GET responses always succeed; configuration polling is outside this test.

To reuse an image already rebuilt from the current checkout:

```bash
python3 integration_test/durable_events.py --no-build --image beelzebub-events:local
```

Add `--fs-group` to verify a root-owned volume with mode `02775` writable through
GID 65532, as provisioned by compatible Kubernetes storage drivers. The test
asserts the sensor preserves that mode. Add `--nested-spool` to create missing
private spool ancestors inside the retained volume and replay them after restart.
Run both with and without `--nested-spool` to cover a spool directly on the mount
and automatic directory creation. Unit tests separately check parent fsync order
and injected failures; SIGKILL alone does not simulate host power loss.

Requires Python 3, Docker Engine, and `python:3.12-slim` (override with
`--python-image`). The fixture acknowledges the matching EventKey; it models API
idempotency and does not establish the durability of a real Platform deployment.

## Protocol capture and replay checks

All five built-in strategies receive the same synchronous durable tracer when
Cloud reporting and a persistent spool directory are configured. HTTPS uses the
HTTP handler; TCP service templates, binary traffic, wire plugins, and TLS
upgrades use the TCP capture path. An unset spool directory keeps legacy delivery.

The Go tests exercise actual protocol handlers and a shared disposable Platform
fixture. For each strategy they check fsynced records before delivery, a failed
HTTP 503 POST retaining those records, closing and reopening the spool, and
recovery replay with identical payloads, UUIDv7 keys, and ordering. The fixture is
in `internal/testutil/durabletest`; it does not start background delivery until
capture has completed. Process/container crash survival is tested separately by
the Docker check above.

| Strategy | Capture and replay cases | Cancellation and teardown cases |
| --- | --- | --- |
| HTTP / HTTPS | Matching requests, fallback, and method rejection; legacy and durable modes | Full/unavailable spool and disconnected clients |
| SSH | Password authentication, commands, interactive sessions; legacy and durable modes | Canceled producer, full spool, configured authentication timeout, spool shutdown, and session end after cancellation |
| TCP | Stateless attempts, idle sessions, matched/unmatched commands, binary bytes, TLS upgrade, and wire metadata | Producer cancellation, full spool, spool shutdown, and bounded timeout/teardown capture |
| MCP | Session-based and modern stateless tool requests; legacy and durable modes | Disconnected tool calls blocked on a full spool, for both transport versions |
| Telnet | Accepted/rejected credentials and matched/unmatched commands; legacy and durable modes | Full-spool capture timeout and durable session end after the connection lifetime expires |

Run the protocol checks on their own:

```bash
go test ./internal/protocols/... -race -count=1 -coverpkg=./internal/protocols/...,./internal/testutil/... -coverprofile coverage.out
go tool cover -func coverage.out
```

Run the full coverage/race suite before submitting changes:

```bash
go test ./... -race -count=1 -coverpkg=./... -coverprofile coverage.out -covermode atomic
python3 scripts/check-protocol-coverage.py coverage.out --minimum 80
```

CI uploads the complete `coverage.out` to Codecov, including protocol files.
Coverage instruments all project packages so calls across package boundaries,
including the shared protocol fixture, contribute to the report.
Its existing filtered quality gate uses a separate `gate.out`. A separate
protocol gate requires at least 80% statement coverage in each of `http.go`,
`ssh.go`, `tcp.go`, `mcp.go`, and `telnet.go`; missing file data fails the gate.
File coverage
percentages include unrelated protocol branches; the capture tests specifically
exercise the changed durable capture and teardown lines. These checks do not
promise 100% branch coverage.

A complete event receives one durable admission attempt even after producer
cancellation. When storage is full or unavailable, cancellation can end waiting
and leave it unaccepted; that failure is logged and counted. Already persisted events remain owned by the spool.
Timeout/teardown evidence gets a fresh capture context bounded to five seconds.
Telnet restores the configured socket deadline after its temporary negotiation
deadline so idle reads can finish and capture session end.
SSH authentication also bounds blocked capture by its configured positive
connection timeout, because the SSH library's authentication context does not
itself carry the socket deadline. A non-positive timeout remains unbounded until
producer cancellation or spool shutdown. At-least-once delivery and the remaining
duplicate window are documented in
[`event-delivery.mdx`](../docs/content/docs/operations/event-delivery.mdx).
