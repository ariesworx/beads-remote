"""Drives `beads-remote mcp` over real stdio, as an MCP client would.

    python3 test/mcp_client.py BIN REPO TOOL=OK [TOOL=OK ...]

Calls each tool in turn and exits non-zero unless its result's "ok" matches
(and isError is its opposite). Used by test/e2e.sh.
"""
import json
import subprocess
import sys

binary, repo, *calls = sys.argv[1:]
p = subprocess.Popen([binary, "-C", repo, "mcp"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)


def send(message):
    p.stdin.write(json.dumps(message) + "\n")
    p.stdin.flush()


def answer(request_id):
    while True:
        line = p.stdout.readline()
        if not line:
            sys.exit("server closed stdout")
        message = json.loads(line)
        if message.get("id") == request_id:
            return message


send({"jsonrpc": "2.0", "id": 1, "method": "initialize",
      "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "e2e", "version": "1"}}})
answer(1)
send({"jsonrpc": "2.0", "method": "notifications/initialized"})
for i, call in enumerate(calls, start=2):
    tool, want = call.split("=")
    want_ok = want == "ok"
    send({"jsonrpc": "2.0", "id": i, "method": "tools/call", "params": {"name": tool, "arguments": {}}})
    result = answer(i)["result"]
    if result["structuredContent"]["ok"] != want_ok or result.get("isError", False) == want_ok:
        sys.exit(f"{tool}: {json.dumps(result)}")
p.stdin.close()
p.wait(timeout=10)
