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
