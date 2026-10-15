"""A minimal language-server client over stdio: just the requests the signals
need (references, call hierarchy, hover). Every wait has a deadline, and any
failure to start, index or answer raises LspUnavailable with the reason, so a
signal is reported as unavailable instead of as a number from a broken server.
"""

import json
import os
import select
import subprocess
import time
import urllib.parse
from dataclasses import dataclass
from pathlib import Path

REQUEST_TIMEOUT_SECONDS = 60.0


class LspUnavailable(Exception):
    """The server could not start, index or answer; str(self) is the reason."""


@dataclass(frozen=True)
class Position:
    line: int  # zero-based
    character: int  # zero-based, UTF-16 code units as the protocol defines


@dataclass(frozen=True)
class CallItem:
    """A function in the call hierarchy; `raw` is the server's item, echoed back in follow-up requests."""

    path: str  # repo-relative, "" when outside the session root
    position: Position
    raw: dict


class LspSession:
    """One server process rooted at `root`. Use as a context manager."""

    def __init__(self, command: tuple[str, ...], root: str, language_id: str, timeout: float = REQUEST_TIMEOUT_SECONDS):
        self.command = command
        self.root = os.path.realpath(root)
        self.language_id = language_id
        self.timeout = timeout
        self.server_version = "unknown"
        self._next_id = 0
        self._buffer = b""
        self._proc: subprocess.Popen[bytes] | None = None

    def __enter__(self) -> "LspSession":
        try:
            self._proc = subprocess.Popen(
                self.command, cwd=self.root, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL
            )
        except OSError as e:
            raise LspUnavailable(f"cannot start {self.command[0]}: {e.strerror or e}") from e
        try:
            init = self._request("initialize", {
                "processId": os.getpid(),
                "rootUri": Path(self.root).as_uri(),
                "capabilities": {"textDocument": {"hover": {"contentFormat": ["plaintext", "markdown"]}}},
            })
            info = init.get("serverInfo") or {}
            self.server_version = f"{info.get('name', self.command[0])} {info.get('version', 'unknown')}"
            self._notify("initialized", {})
        except BaseException:
            self.__exit__(None, None, None)
            raise
        return self

    def __exit__(self, *_exc: object) -> None:
        proc = self._proc
        if proc is None:
            return
        self._proc = None
        proc.kill()
        proc.wait()
        for pipe in (proc.stdin, proc.stdout):
            if pipe is not None:
                pipe.close()

    def open_file(self, rel: str) -> None:
        with open(os.path.join(self.root, rel), encoding="utf-8") as f:
            text = f.read()
        self._notify("textDocument/didOpen", {"textDocument": {
            "uri": self._uri(rel), "languageId": self.language_id, "version": 1, "text": text,
        }})

    def reference_count(self, rel: str, at: Position) -> int:
        """References to the symbol at `at`, its own declaration excluded."""
        found = self._request("textDocument/references", {
            **self._at(rel, at), "context": {"includeDeclaration": False},
        })
        return len(found or [])

    def prepare_call_hierarchy(self, rel: str, at: Position) -> list[CallItem]:
        """The callable at `at`; empty when it is not a function."""
        return [self._call_item(raw) for raw in self._request("textDocument/prepareCallHierarchy", self._at(rel, at)) or []]

    def outgoing_calls(self, item: CallItem) -> list[CallItem]:
        calls = self._request("callHierarchy/outgoingCalls", {"item": item.raw}) or []
        return [self._call_item(call["to"]) for call in calls]

    def hover_signature(self, rel: str, at: Position) -> str:
        """The declaration line the server shows on hover: its first code block, or all of it when there is none."""
        contents = (self._request("textDocument/hover", self._at(rel, at)) or {}).get("contents", "")
        if isinstance(contents, list):
            contents = contents[0] if contents else ""
        text = contents.get("value", "") if isinstance(contents, dict) else contents
        if "```" in text:
            text = text.split("```")[1].partition("\n")[2]
        return text.strip()

    def _uri(self, rel: str) -> str:
        return Path(self.root, rel).as_uri()

    def _at(self, rel: str, at: Position) -> dict:
        return {"textDocument": {"uri": self._uri(rel)}, "position": {"line": at.line, "character": at.character}}

    def _call_item(self, raw: dict) -> CallItem:
        path = os.path.relpath(urllib.parse.unquote(urllib.parse.urlparse(raw["uri"]).path), self.root)
        start = raw["selectionRange"]["start"]
        return CallItem("" if path.startswith("..") else path, Position(start["line"], start["character"]), raw)

    def _notify(self, method: str, params: dict) -> None:
        self._send({"jsonrpc": "2.0", "method": method, "params": params})

    def _request(self, method: str, params: dict) -> dict | list | None:
        self._next_id += 1
        request_id = self._next_id
        self._send({"jsonrpc": "2.0", "id": request_id, "method": method, "params": params})
        deadline = time.monotonic() + self.timeout
        while True:
            message = self._receive(deadline, method)
            if "method" in message:
                if "id" in message:  # the server asking us something; answer with what we have none of
                    asked = message["params"].get("items", []) if message["method"] == "workspace/configuration" else None
                    self._send({"jsonrpc": "2.0", "id": message["id"], "result": [None] * len(asked) if asked is not None else None})
            elif message.get("id") == request_id:
                if "error" in message:
                    raise LspUnavailable(f"{method} failed: {message['error'].get('message', message['error'])}")
                return message.get("result")

    def _send(self, message: dict) -> None:
        assert self._proc is not None and self._proc.stdin is not None
        body = json.dumps(message).encode()
        try:
            self._proc.stdin.write(b"Content-Length: %d\r\n\r\n" % len(body) + body)
            self._proc.stdin.flush()
        except OSError as e:
            raise LspUnavailable(f"{self.command[0]} exited: {e.strerror or e}") from e

    def _receive(self, deadline: float, method: str) -> dict:
        while True:
            head, separator, rest = self._buffer.partition(b"\r\n\r\n")
            if separator:
                length = next(int(h.split(b":")[1]) for h in head.split(b"\r\n") if h.lower().startswith(b"content-length"))
                if len(rest) >= length:
                    self._buffer = rest[length:]
                    return json.loads(rest[:length])
            self._buffer += self._read_some(deadline, method)

    def _read_some(self, deadline: float, method: str) -> bytes:
        assert self._proc is not None and self._proc.stdout is not None
        remaining = deadline - time.monotonic()
        if remaining <= 0 or not select.select([self._proc.stdout], [], [], remaining)[0]:
            raise LspUnavailable(f"{self.command[0]} did not answer {method} within {self.timeout:g}s")
        chunk = os.read(self._proc.stdout.fileno(), 65536)
        if not chunk:
            raise LspUnavailable(f"{self.command[0]} exited during {method}")
        return chunk
