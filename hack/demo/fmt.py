#!/usr/bin/env python3
"""Condense controller log lines into one readable line each, for the demo.

Reads the raw manager log on stdin and writes a timestamp plus the single fact
that matters per event, so the audience reads outcomes rather than JSON.
"""
import datetime
import re
import sys

PATTERNS = {
    "when": r"(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)",
    "logBytes": r'"logBytes": (\d+)',
    "eventCount": r'"eventCount": (\d+)',
    "action": r'"action": "([a-z_]+)"',
    "result": r'"result": "([a-z_]+)"',
    "mttr": r'"mttr": "([^"]+)"',
}


def field(name, line, default="?"):
    found = re.search(PATTERNS[name], line)
    return found.group(1) if found else default


def local_time(line):
    """The controller logs UTC. Show local time so it matches the wall clock
    you are narrating against."""
    stamp = field("when", line, None)
    if not stamp:
        return "--:--:--"
    utc = datetime.datetime.strptime(stamp, "%Y-%m-%dT%H:%M:%S")
    utc = utc.replace(tzinfo=datetime.timezone.utc)
    return utc.astimezone().strftime("%H:%M:%S")


for line in sys.stdin:
    when = local_time(line)
    if "DETECTED CrashLoop" in line:
        print(when + "  DETECTED crash loop")
    elif "Collected incident" in line:
        logs = field("logBytes", line)
        events = field("eventCount", line)
        print(when + "  evidence collected   logBytes=" + logs + "  events=" + events)
    elif "remediation finished" in line:
        action = field("action", line)
        result = field("result", line).upper()
        mttr = field("mttr", line)
        print(when + "  " + action + " -> " + result + "   mttr=" + mttr)
    elif "EXHAUSTED" in line:
        print(when + "  *** EXHAUSTED — attempt budget spent, going quiet ***")
    elif "ESCALATED" in line:
        print(when + "  ESCALATED — not safe for automation")
    sys.stdout.flush()
