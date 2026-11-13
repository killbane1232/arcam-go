#!/usr/bin/env python3
"""Read-only Arcam Streamable HTTP check; Python 3.9+, standard library only."""

import argparse
import json
import os
import sys
import urllib.error
import urllib.request


REQUIRED_TOOLS = {
    "get_catalog", "list_strategies", "validate_strategy", "save_strategy",
    "list_datasets", "import_history", "run_backtest", "list_backtests",
    "get_backtest", "get_decisions", "compare_backtests", "cancel_backtest",
}


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class Client:
    def __init__(self, url, token="", timeout=15):
        self.url = url
        self.timeout = timeout
        self.sequence = 0
        self.headers = {
            "Content-Type": "application/json",
            "Accept": "application/json, text/event-stream",
        }
        if token:
            self.headers["Authorization"] = "Bearer " + token
        # Do not forward an authorization header to a redirect target.
        self.opener = urllib.request.build_opener(NoRedirect())

    def rpc(self, method, params=None, notification=False):
        message = {"jsonrpc": "2.0", "method": method}
        if params is not None:
            message["params"] = params
        if not notification:
            self.sequence += 1
            message["id"] = self.sequence
        request = urllib.request.Request(
            self.url, json.dumps(message).encode(), self.headers, method="POST"
        )
        try:
            with self.opener.open(request, timeout=self.timeout) as response:
                body = response.read()
                content_type = response.headers.get("Content-Type", "")
                session = response.headers.get("Mcp-Session-Id")
                if session:
                    self.headers["Mcp-Session-Id"] = session
        except urllib.error.HTTPError as exc:
            hints = {
                401: "Check the API token and --token-env.",
                403: "Check token, Host/Origin and ARCAM_ALLOWED_HOSTS.",
                404: "Use the endpoint ending in /mcp.",
                405: "Use POST on /mcp, not the web UI root.",
            }
            raise RuntimeError("HTTP %s. %s" % (
                exc.code, hints.get(exc.code, "Check the server and endpoint.")
            )) from None
        if notification and not body:
            return None
        if "application/json" not in content_type:
            raise RuntimeError(
                "Expected Arcam JSONResponse; check /mcp and server version "
                "(this diagnostic does not implement SSE)."
            )
        result = json.loads(body)
        if result.get("jsonrpc") != "2.0" or result.get("id") != message.get("id"):
            raise RuntimeError("Invalid JSON-RPC response or mismatched request ID")
        if "error" in result:
            raise RuntimeError("JSON-RPC error: " + json.dumps(result["error"]))
        return result["result"]

    def initialize(self):
        result = self.rpc("initialize", {
            "protocolVersion": "2025-03-26", "capabilities": {},
            "clientInfo": {"name": "arcam-readonly-check", "version": "1.0.0"},
        })
        self.headers["MCP-Protocol-Version"] = result["protocolVersion"]
        self.rpc("notifications/initialized", notification=True)
        return result

    def call(self, name, arguments=None):
        result = self.rpc("tools/call", {"name": name, "arguments": arguments or {}})
        if result.get("isError"):
            raise RuntimeError("%s: %s" % (name, json.dumps(result.get("content"))))
        if "structuredContent" in result:
            return result["structuredContent"]
        for block in result.get("content", []):
            if block.get("type") == "text":
                return json.loads(block["text"])
        raise RuntimeError(name + ": missing JSON tool result")


def check(client):
    init = client.initialize()
    if init.get("serverInfo", {}).get("name") != "arcam":
        raise RuntimeError("Endpoint did not identify itself as Arcam")
    names, cursor, seen = set(), None, set()
    for _ in range(50):
        page = client.rpc("tools/list", {"cursor": cursor} if cursor else {})
        names.update(tool["name"] for tool in page["tools"])
        cursor = page.get("nextCursor")
        if not cursor:
            break
        if cursor in seen:
            raise RuntimeError("tools/list repeated a pagination cursor")
        seen.add(cursor)
    else:
        raise RuntimeError("tools/list exceeded 50 pages")
    missing = REQUIRED_TOOLS - names
    if missing:
        raise RuntimeError("Missing tools: " + ", ".join(sorted(missing)))
    catalog = client.call("get_catalog")
    validation = client.call("validate_strategy", {
        "strategy_json": json.dumps(catalog["example"])
    })
    if validation.get("valid") is not True:
        raise RuntimeError("Catalog example failed validation")
    return {
        "ok": True, "mode": "read_only", "server": init["serverInfo"],
        "protocol_version": init["protocolVersion"], "tools": sorted(names),
        "catalog_example_warmup": validation["warmup"],
        "strategy_versions": len(client.call("list_strategies")["versions"] or []),
        "datasets": len(client.call("list_datasets")["datasets"] or []),
        "backtests": len(client.call("list_backtests")["runs"] or []),
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", default=os.getenv("ARCAM_MCP_URL", "http://127.0.0.1:8080/mcp"))
    parser.add_argument("--token-env", default="ARCAM_API_TOKEN", help="Environment variable name; never pass the token itself")
    parser.add_argument("--timeout", type=float, default=15)
    args = parser.parse_args()
    if args.timeout <= 0:
        parser.error("--timeout must be positive")
    try:
        result = check(Client(args.url, os.getenv(args.token_env, ""), args.timeout))
    except (RuntimeError, OSError, ValueError, KeyError, TypeError) as exc:
        print(json.dumps({"ok": False, "error": str(exc)}, ensure_ascii=False), file=sys.stderr)
        return 1
    print(json.dumps(result, indent=2, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
