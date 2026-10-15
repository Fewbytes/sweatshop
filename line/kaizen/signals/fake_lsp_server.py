"""A language server that answers from a JSON script, for the tests of lsp.py
and lsp_signals.py. Usage: fake_lsp_server.py SCRIPT.json

Script keys, where a position is "path:line:character" relative to the root:
  references: position -> number of references returned
  hover: position -> hover text
  calls: position -> positions it calls; a position here is a callable function
  mode: "serve" (default), "exit" (die after initialize) or "silent" (never answer after initialize)
"""

import json
import sys


def read_message():
    length = 0
    while (line := sys.stdin.buffer.readline().strip()):
        if line.lower().startswith(b"content-length"):
            length = int(line.split(b":")[1])
    return json.loads(sys.stdin.buffer.read(length)) if length else None


def send(message):
    body = json.dumps(message).encode()
    sys.stdout.buffer.write(b"Content-Length: %d\r\n\r\n" % len(body) + body)
    sys.stdout.buffer.flush()


def main():
    script = json.load(open(sys.argv[1]))
    root = ""

    def key_of(uri, position):
        return f"{uri.removeprefix(root).lstrip('/')}:{position['line']}:{position['character']}"

    def item(key):
        path, line, character = key.rsplit(":", 2)
        at = {"line": int(line), "character": int(character)}
        uri = root + "/" + path if not path.startswith("/") else "file://" + path
        return {"name": key, "kind": 12, "uri": uri, "range": {"start": at, "end": at}, "selectionRange": {"start": at, "end": at}}

    while (message := read_message()) is not None:
        method, params = message.get("method"), message.get("params", {})
        result = None
        if method == "initialize":
            root = params["rootUri"]
            result = {"capabilities": {}, "serverInfo": {"name": "fake-lsp", "version": "1.2.3"}}
        elif "id" not in message:
            continue
        elif script.get("mode") == "exit":
            sys.exit(0)
        elif script.get("mode") == "silent":
            continue
        elif method == "textDocument/references":
            key = key_of(params["textDocument"]["uri"], params["position"])
            result = [{"uri": root, "range": {}}] * script["references"].get(key, 0)
        elif method == "textDocument/hover":
            key = key_of(params["textDocument"]["uri"], params["position"])
            result = {"contents": {"kind": "markdown", "value": "```\n" + script["hover"][key] + "\n```\n\ndocs"}}
        elif method == "textDocument/prepareCallHierarchy":
            key = key_of(params["textDocument"]["uri"], params["position"])
            result = [item(key)] if key in script["calls"] else None
        elif method == "callHierarchy/outgoingCalls":
            result = [{"to": item(callee), "fromRanges": []} for callee in script["calls"][params["item"]["name"]]]
        send({"jsonrpc": "2.0", "id": message["id"], "result": result})


main()
