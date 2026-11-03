import json
import logging
import os
import time
from collections import defaultdict

import requests

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))


#load KEY=VALUE pairs from the .env file next to this script
def load_env():
    with open(os.path.join(SCRIPT_DIR, ".env")) as f:
        for line in f:
            line = line.strip()
            if line and not line.startswith("#") and "=" in line:
                key, value = line.split("=", 1)
                value = value.strip().strip('"').strip("'") #allow KEY="value" as well as KEY=value
                os.environ.setdefault(key.strip(), value)


load_env()
WEBHOOK_TYPE = os.environ["WEBHOOK_TYPE"].lower()  #"discord" or "slack"
WEBHOOK_URL = os.environ["WEBHOOK_URL"]
LOG_FILE = os.path.join(SCRIPT_DIR, os.environ.get("LOG_FILE", "../../logs/beelzebub.log"))

#!!! IMPORTANT - WINDOWS / DOCKER DESKTOP IP PROBLEM !!!
#on windows, docker desktop hides the attacker's real IP: every event shows the docker
#gateway (e.g. 172.17.0.1 or 192.168.65.x) as the SourceIp instead.
#because the cooldown below is per IP, all attackers get merged into one "IP", so after
#the first alert everything else is suppressed for COOLDOWN_SECONDS.
#  - workaround for local testing on windows: set COOLDOWN_SECONDS = 0
#  - real fix: run the docker stack on a linux host/VM, where real IPs come through
#    (no code changes needed, set COOLDOWN_SECONDS back to 300 there)

#filter settings
IGNORE_PATHS = {"/example.ico"}
PROBE_THRESHOLD = 1                     #number of probes to alert on, set to 1 for testing
PROBE_WINDOW_SECONDS = 60               #the given threshhold for probes
COOLDOWN_SECONDS = 300

#in-memory state
last_alerted = {}
request_times = defaultdict(list)


#alert filter
def should_alert(ip, path):
    now = time.time()

    if path in IGNORE_PATHS:
        return False

    #record this request, then drop anything outside the window
    request_times[ip].append(now)
    request_times[ip] = [t for t in request_times[ip] if now - t <= PROBE_WINDOW_SECONDS]

    if len(request_times[ip]) < PROBE_THRESHOLD:
        return False #too few requests yet to call this active probing

    if ip in last_alerted and now - last_alerted[ip] < COOLDOWN_SECONDS:
        return False #already alerted on this IP recently

    last_alerted[ip] = now
    return True

#alert message
def format_message(details, count):
    #discord bolds with **text**, slack bolds with *text*
    b = "**" if WEBHOOK_TYPE == "discord" else "*"
    return (
        f"Active probing found\n"
        f"{b}Time:{b} {details.get('DateTime')}\n"
        f"{b}Protocol:{b} {details.get('Protocol')}\n"
        f"{b}Source IP:{b} {details.get('SourceIp')}\n"
        f"{b}Request:{b} {details.get('HTTPMethod', '')} {details.get('RequestURI') or details.get('Command', '')}\n"
        f"{b}Service:{b} {details.get('Description', '')}\n"
        f"{b}Response given:{b} {details.get('CommandOutput', '')[:500]}\n" #cut long LLM replies short
        f"{b}Probes in last {PROBE_WINDOW_SECONDS}s:{b} {count}"
    )


#discord or slack output decider
def send_alert(text):
    #each service expects a different JSON key for the message
    if WEBHOOK_TYPE == "discord":
        payload = {"content": text[:2000]}  #discord rejects messages over 2000 chars
    elif WEBHOOK_TYPE == "slack":
        payload = {"text": text}
    else:
        raise ValueError(f"Unknown WEBHOOK_TYPE: {WEBHOOK_TYPE}")

    #catch network errors so one failed post doesn't kill the loop
    try:
        r = requests.post(WEBHOOK_URL, json=payload, timeout=10)
        if r.status_code >= 300:
            print(f"Webhook returned {r.status_code}: {r.text}")
    except requests.RequestException as e:
        print(f"Failed to send alert: {e}")


#grabs code from the log output
def handle_line(line):
    try:
        entry = json.loads(line)
    except json.JSONDecodeError:
        return #not a JSON line, skip it

    # JSON scalars/arrays are legal JSON but not Beelzebub log objects.
    if not isinstance(entry, dict):
        logging.warning("Skipping JSON log entry that is not an object")
        return

    #only beelzebub's "New Event" lines are attacker activity, skip startup/info logs
    if entry.get("msg") != "New Event":
        return

    details = entry.get("event", {})
    if not isinstance(details, dict):
        logging.warning("Skipping New Event with non-object event payload")
        return

    ip = details.get("SourceIp", "unknown")
    path = details.get("RequestURI", "")

    if should_alert(ip, path):
        count = len(request_times[ip])
        send_alert(format_message(details, count))
        print(f"Alert sent for {ip} ({count} probes)")
    else:
        print(f"Suppressed event from {ip} ({path})")


#long running loop: follow the log file like `tail -f`
#
#!!! IMPORTANT - TODO: FIX LATER !!!
#beelzebub.log is never rotated, so it grows forever and will eventually fill the disk.
#do NOT delete the log while the containers are running: this loop keeps reading the
#old (deleted) file and silently stops alerting. to clear it for now:
#  docker compose stop -> delete logs/beelzebub.log -> docker compose start
#proper fix later: add log rotation and make this loop reopen the file when it shrinks/changes.
def follow_log():
    #wait for beelzebub to create the log file
    while not os.path.exists(LOG_FILE):
        print(f"Waiting for {LOG_FILE} ...")
        time.sleep(5)

    with open(LOG_FILE, encoding="utf-8", errors="replace") as f:
        f.seek(0, os.SEEK_END) #start at the end so old events aren't re-alerted
        print(f"Watching {LOG_FILE} ({WEBHOOK_TYPE} webhook)")
        while True:
            line = f.readline()
            if not line:
                time.sleep(1) #nothing new yet
                continue
            try:
                handle_line(line)
            except Exception:
                # One malformed event must not permanently stop log monitoring.
                logging.exception("Failed to process honeypot log entry; continuing")


if __name__ == "__main__":
    follow_log()
