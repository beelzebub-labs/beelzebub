#!/usr/bin/env python3
"""Rebuild and verify durable events using disposable Docker resources only."""

import argparse
import base64
import json
from pathlib import Path
import signal
import subprocess
import time
import uuid


ROOT = Path(__file__).resolve().parents[1]
FIXTURE = ROOT / "integration_test" / "durable_events_fixture.py"


def docker(*arguments, timeout=120, check=True):
    result = subprocess.run(["docker", *map(str, arguments)], check=False, text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)

    if check and result.returncode:
        raise RuntimeError("docker " + str(arguments[0]) + " failed:\n" + result.stdout[-8000:] + result.stderr[-8000:])

    return result


def eventually(check, description, timeout=150):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if check():
            return

        time.sleep(0.5)

    raise AssertionError("Timed out: " + description)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", help="Local sensor image tag; default is a unique test tag")
    parser.add_argument("--no-build", action="store_true", help="Use an already rebuilt --image")
    parser.add_argument("--python-image", default="python:3.12-slim")
    parser.add_argument("--fs-group", action="store_true", help="Verify a root-owned 02775 volume writable through GID 65532")
    parser.add_argument("--nested-spool", action="store_true", help="Create missing spool ancestors inside the retained volume")
    args = parser.parse_args()

    if args.no_build and not args.image:
        parser.error("--no-build requires --image")

    prefix = "beelzebub-durable-ci-" + uuid.uuid4().hex[:12]
    image = args.image or prefix + ":local"
    network, volume = prefix + "-network", prefix + "-spool"
    api, sensor = prefix + "-api", prefix + "-sensor"
    containers = set()
    network_created = volume_created = image_created = False
    spool_suffix = "/state/sensor/events" if args.nested_spool else ""

    def terminate(_signal, _frame):
        raise SystemExit("Interrupted; cleaning up isolated test resources")

    signal.signal(signal.SIGTERM, terminate)

    def fixture(mode, *arguments):
        result = docker("exec", api, "python", "/fixture.py", mode, *arguments)
        return json.loads(result.stdout)

    def state():
        return fixture("request", "/state")

    def control(**values):
        fixture("request", "/control", json.dumps(values))

    def snapshot():
        result = docker("run", "--rm", "--network", "none", "-v", volume + ":/spool:ro",
                        "-v", str(FIXTURE) + ":/fixture.py:ro", args.python_image,
                        "python", "/fixture.py", "snapshot", "/spool" + spool_suffix)

        return json.loads(result.stdout)

    def start_sensor():
        options = ["run", "-d", "--name", sensor, "--network", network,
                   "--network-alias", "sensor", "--user", "65532:65532", "--cap-drop", "ALL",
                   "--security-opt", "no-new-privileges", "-v", volume + ":/var/lib/beelzebub/events"]

        environment = {
            "BEELZEBUB_CLOUD_ENABLED": "true",
            "BEELZEBUB_CLOUD_URI": "http://platform:8080/cloud",
            "BEELZEBUB_CLOUD_AUTH_TOKEN": "00000000-0000-4000-8000-000000000001",
            "BEELZEBUB_EVENT_SPOOL_DIR": "/var/lib/beelzebub/events" + spool_suffix,
            "BEELZEBUB_SERVICES_CONFIG": "[]",
        }

        for key, value in environment.items():
            options.extend(["-e", key + "=" + value])

        containers.add(sensor)
        docker(*options, image)

    try:
        if not args.no_build:
            print("Building local sensor image " + image, flush=True)
            docker("build", "-f", ROOT / "Dockerfile", "-t", image, ROOT, timeout=900)
            image_created = not args.image

        docker("network", "create", "--internal", network)
        network_created = True
        docker("volume", "create", volume)
        volume_created = True
        provision = ("import os; os.chown('/spool',0,65532); os.chmod('/spool',0o2775)" if args.fs_group else
                     "import os; os.chown('/spool',65532,65532); os.chmod('/spool',0o700)")
        docker("run", "--rm", "--network", "none", "--user", "0:0", "-v", volume + ":/spool",
               args.python_image, "python", "-c",
               provision)
        containers.add(api)
        docker("run", "-d", "--name", api, "--network", network, "--network-alias", "platform",
               "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
               "-v", str(FIXTURE) + ":/fixture.py:ro", args.python_image,
               "python", "/fixture.py", "serve")

        def api_ready():
            result = docker("exec", api, "python", "/fixture.py", "request", "/state", check=False)
            return result.returncode == 0

        eventually(api_ready, "synthetic Platform readiness", timeout=30)
        start_sensor()

        def capture_ready():
            result = docker("exec", api, "python", "/fixture.py", "trigger", "initial", check=False)
            return result.returncode == 0

        eventually(capture_ready, "unprivileged sensor capture readiness", timeout=30)
        if args.fs_group:
            docker("run", "--rm", "--network", "none", "-v", volume + ":/spool:ro",
                   args.python_image, "python", "-c",
                   "import os; s=os.stat('/spool'); assert (s.st_uid,s.st_gid,s.st_mode & 0o7777)==(0,65532,0o2775)")

            print("Root-owned fsGroup volume mode 02775 preserved; non-root capture succeeded", flush=True)

        if args.nested_spool:
            docker("run", "--rm", "--network", "none", "-v", volume + ":/spool:ro",
                   args.python_image, "python", "-c",
                   "import os; from pathlib import Path; paths=[Path('/spool/state'),Path('/spool/state/sensor'),Path('/spool/state/sensor/events')]; assert all(p.stat().st_uid==65532 and p.stat().st_mode & 0o777==0o700 for p in paths)")

            print("Missing spool ancestors created privately by UID 65532", flush=True)

        for status in (500, 502, 503, 504):
            control(status=status)
            fixture("trigger", str(status))

            eventually(lambda: any(a["status"] == status for a in state()["attempts"]),
                       "event retry after HTTP " + str(status))

        before = snapshot()
        assert len(before) >= 5, "every accepted capture must be pending during the outage"
        assert all(r["uid"] == 65532 and r["mode"] == 0o600 for r in before), "spool ownership/permissions"
        assert all(uuid.UUID(r["key"]).version == 7 for r in before), "new captures must use UUIDv7"
        original = {record["key"]: record["wirePayload"] for record in before}
        assert len(original) == len(before), "individual captures must have distinct identities"
        attempts = state()["attempts"]
        assert len({attempt["key"] for attempt in attempts}) == 1, "outage retries must stay on the oldest event"
        assert attempts[0]["key"] == min(original), "first capture must remain the replay head"
        print("HTTP 500/502/503/504 retained " + str(len(original)) + " events as UID 65532", flush=True)

        docker("kill", "--signal", "KILL", sensor)
        docker("rm", sensor)
        assert snapshot() == before, "SIGKILL/removal must retain the unchanged spool"
        start_sensor()
        control(status=200)
        eventually(lambda: not snapshot(), "recreated container draining retained volume")
        accepted = state()["accepted"]

        for key, payload in original.items():
            assert accepted[key] == payload, "replay changed original payload/key bytes"

        for attempt in state()["attempts"]:
            if attempt["key"] in original:
                assert attempt["payload"] == original[attempt["key"]]

        print("SIGKILL, container removal/recreation, original payload/key replay, and volume drain passed", flush=True)
        replayed = [a["key"] for a in state()["attempts"] if a["status"] == 200 and a["key"] in original]
        assert replayed == sorted(original), "retained UUIDv7 events must drain oldest first after recreation"
        print("UUIDv7 identities and oldest-first replay across 5xx failures and restart passed", flush=True)

        fixture("trigger-large", "large-ack")
        eventually(lambda: not snapshot(), "large EventDTO acknowledgement draining")
        large = [key for key, body in state()["accepted"].items()
                 if len(json.loads(base64.b64decode(body)).get("Body", "")) == 70000]

        assert len(large) == 1, "large accepted EventDTO must be acknowledged and removed"
        assert sum(a["key"] == large[0] for a in state()["attempts"]) == 1
        print("Full EventDTO acknowledgement exceeding 64 KiB drained without blocking delivery", flush=True)

        control(lost_ack=True)
        fixture("trigger", "lost-ack")
        eventually(lambda: bool(state()["dropped"]), "acceptance followed by lost HTTP acknowledgement")
        lost_key = state()["dropped"][0]
        eventually(lambda: sum(a["key"] == lost_key for a in state()["attempts"]) >= 2,
                   "stable identity retry after lost acknowledgement")

        eventually(lambda: not snapshot(), "lost acknowledgement event draining")
        copies = [a["payload"] for a in state()["attempts"] if a["key"] == lost_key]
        assert len(set(copies)) == 1, "ambiguous acknowledgement changed payload"
        print("Lost acknowledgement retried the identical event; all durable container checks passed", flush=True)
    except BaseException:
        if sensor in containers:
            logs = docker("logs", "--tail", "50", sensor, check=False)
            print(logs.stdout + logs.stderr)
        raise
    finally:
        for container in containers:
            docker("rm", "-f", container, check=False)

        if volume_created:
            docker("volume", "rm", volume, check=False)

        if network_created:
            docker("network", "rm", network, check=False)

        if image_created:
            docker("image", "rm", image, check=False)


if __name__ == "__main__":
    main()
