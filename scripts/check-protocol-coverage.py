#!/usr/bin/env python3
"""Require individual statement coverage for every built-in protocol strategy."""

import argparse
from pathlib import Path


PROTOCOLS = ("HTTP", "SSH", "TCP", "MCP", "TELNET")


def check(profile: Path, minimum: float) -> int:
    # -coverpkg can report the same block from several test binaries. Sum hits
    # before counting statements so a cross-package hit is not treated as missing.
    blocks = {}
    for line in profile.read_text().splitlines()[1:]:
        location, statements, hits = line.split()
        count, previous_hits = blocks.get(location, (int(statements), 0))
        if count != int(statements):
            raise ValueError(f"inconsistent statement count for {location}")
        blocks[location] = (count, previous_hits + int(hits))

    failed = False

    for protocol in PROTOCOLS:
        suffix = f"internal/protocols/strategies/{protocol}/{protocol.lower()}.go"
        rows = [values for location, values in blocks.items()
                if location.rsplit(":", 1)[0].endswith(suffix)]

        total = sum(count for count, _ in rows)
        covered = sum(count for count, hits in rows if hits > 0)
        if total == 0:
            print(f"FAIL {suffix}: no executable coverage data")
            failed = True
            continue

        passed = covered * 100 >= minimum * total
        print(f"{'PASS' if passed else 'FAIL'} {suffix}: "
              f"{100 * covered / total:.2f}% ({covered}/{total}); minimum {minimum:g}%")

        failed |= not passed
    return int(failed)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("profile", type=Path)
    parser.add_argument("--minimum", type=float, default=80)
    args = parser.parse_args()

    if not 0 <= args.minimum <= 100:
        parser.error("--minimum must be between 0 and 100")

    raise SystemExit(check(args.profile, args.minimum))
