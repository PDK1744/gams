#!/usr/bin/env python3
"""
GAMS MCP Client Agent
A standalone Python MCP client over Streamable HTTP (SSE + POST) using the Python standard library.
"""

import argparse
import json
import queue
import sys
import threading
import time
import urllib.request
import urllib.error

class MCPAgent:
    def __init__(self, name: str, base_url: str, api_key: str):
        self.name = name
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.session_id = None
        self.post_url = None
        self.sse_resp = None
        self.running = True
        self.msg_queue = queue.Queue()
        self.pending_requests = {}
        self.lock = threading.Lock()
        self.req_counter = 0

    def connect(self):
        print(f"[{self.name}] Connecting to GAMS MCP SSE at {self.base_url}/mcp/sse ...")
        req = urllib.request.Request(
            f"{self.base_url}/mcp/sse",
            headers={"Authorization": f"Bearer {self.api_key}"}
        )
        try:
            self.sse_resp = urllib.request.urlopen(req)
        except urllib.error.HTTPError as e:
            print(f"[{self.name}] SSE connection failed: HTTP {e.code} - {e.read().decode('utf-8')}", file=sys.stderr)
            sys.exit(1)
        except Exception as e:
            print(f"[{self.name}] SSE connection error: {e}", file=sys.stderr)
            sys.exit(1)

        # Read initial endpoint event
        event_name = None
        data_lines = []
        for raw_line in self.sse_resp:
            line = raw_line.decode("utf-8").rstrip("\r\n")
            if not line:
                if event_name == "endpoint":
                    endpoint = "\n".join(data_lines)
                    if endpoint.startswith("/"):
                        self.post_url = f"{self.base_url}{endpoint}"
                    else:
                        self.post_url = endpoint
                    # Extract session_id
                    if "session_id=" in self.post_url:
                        self.session_id = self.post_url.split("session_id=")[1].split("&")[0]
                    break
                event_name = None
                data_lines = []
                continue

            if line.startswith("event: "):
                event_name = line[7:]
            elif line.startswith("data: "):
                data_lines.append(line[6:])

        if not self.post_url:
            print(f"[{self.name}] Failed to receive endpoint event from SSE", file=sys.stderr)
            sys.exit(1)

        print(f"[{self.name}] Connected! Session ID: {self.session_id}")
        print(f"[{self.name}] Message Endpoint: {self.post_url}")

        # Start background listener thread for incoming SSE messages
        self.listener_thread = threading.Thread(target=self._sse_listener, daemon=True)
        self.listener_thread.start()

        # Perform MCP initialization
        self._initialize()

    def _sse_listener(self):
        event_name = None
        data_lines = []
        try:
            for raw_line in self.sse_resp:
                if not self.running:
                    break
                line = raw_line.decode("utf-8").rstrip("\r\n")
                if not line:
                    if event_name == "message" and data_lines:
                        payload = "\n".join(data_lines)
                        msg_data = json.loads(payload)
                        items = msg_data if isinstance(msg_data, list) else [msg_data]
                        for item in items:
                            req_id = item.get("id") if isinstance(item, dict) else None
                            with self.lock:
                                q = self.pending_requests.get(req_id)
                            if q is not None:
                                q.put(item)
                            else:
                                self.msg_queue.put(item)
                    event_name = None
                    data_lines = []
                    continue

                if line.startswith(":"):
                    # keepalive heartbeat comment
                    continue
                if line.startswith("event: "):
                    event_name = line[7:]
                elif line.startswith("data: "):
                    data_lines.append(line[6:])
        except Exception:
            pass

    def call_rpc(self, method: str, params: dict = None, timeout: float = 10.0) -> dict:
        self.req_counter += 1
        req_id = self.req_counter
        payload = {
            "jsonrpc": "2.0",
            "id": req_id,
            "method": method
        }
        if params is not None:
            payload["params"] = params

        data_bytes = json.dumps(payload).encode("utf-8")
        req = urllib.request.Request(
            self.post_url,
            data=data_bytes,
            headers={
                "Content-Type": "application/json",
                "Mcp-Session-Id": self.session_id,
            }
        )

        resp_queue = queue.Queue()
        with self.lock:
            self.pending_requests[req_id] = resp_queue

        try:
            try:
                with urllib.request.urlopen(req) as resp:
                    if resp.status != 202:
                        print(f"[{self.name}] Warning: unexpected HTTP status {resp.status}", file=sys.stderr)
            except urllib.error.HTTPError as e:
                print(f"[{self.name}] POST message error: HTTP {e.code} - {e.read().decode('utf-8')}", file=sys.stderr)
                raise

            # Wait for response on dedicated SSE response queue
            try:
                return resp_queue.get(timeout=timeout)
            except queue.Empty:
                raise TimeoutError(f"Timed out waiting for response to {method} (id={req_id})")
        finally:
            with self.lock:
                self.pending_requests.pop(req_id, None)

    def notify(self, method: str, params: dict = None):
        payload = {
            "jsonrpc": "2.0",
            "method": method
        }
        if params is not None:
            payload["params"] = params

        data_bytes = json.dumps(payload).encode("utf-8")
        req = urllib.request.Request(
            self.post_url,
            data=data_bytes,
            headers={
                "Content-Type": "application/json",
                "Mcp-Session-Id": self.session_id,
            }
        )
        with urllib.request.urlopen(req) as resp:
            pass

    def _initialize(self):
        resp = self.call_rpc("initialize", {
            "protocolVersion": "2024-11-05",
            "clientInfo": {"name": self.name, "version": "1.0"}
        })
        server_info = resp.get("result", {}).get("serverInfo", {})
        print(f"[{self.name}] Handshake OK: Server={server_info.get('name')} v{server_info.get('version')} ({server_info.get('description')})")
        self.notify("notifications/initialized")

    def remember(self, content: str, confidence: float, evidence_uri: str, evidence_desc: str, namespace: str = "default", tags: list = None) -> dict:
        print(f"[{self.name}] -> Calling tool 'remember' in namespace '{namespace}'...")
        args = {
            "content": content,
            "confidence": confidence,
            "evidence": [{
                "uri": evidence_uri,
                "description": evidence_desc,
                "evidence_type": "agent_assertion"
            }],
            "namespace": namespace,
            "tags": tags or []
        }
        resp = self.call_rpc("tools/call", {"name": "remember", "arguments": args})
        result = resp.get("result", {})
        if result.get("isError"):
            print(f"[{self.name}] Error from remember: {result.get('content', [{}])[0].get('text')}", file=sys.stderr)
            return None
        text_data = result.get("content", [{}])[0].get("text", "{}")
        data = json.loads(text_data)
        print(f"[{self.name}] OK Claim stored: ID={data.get('claim_id')} (version={data.get('version')}, status={data.get('status')})")
        return data

    def recall(self, query: str, namespace: str = None) -> dict:
        print(f"[{self.name}] -> Calling tool 'recall' for query: {query!r} (namespace={namespace or 'ALL'})...")
        args = {"query": query}
        if namespace:
            args["namespace"] = namespace
        resp = self.call_rpc("tools/call", {"name": "recall", "arguments": args})
        result = resp.get("result", {})
        if result.get("isError"):
            print(f"[{self.name}] Error from recall: {result.get('content', [{}])[0].get('text')}", file=sys.stderr)
            return None
        text_data = result.get("content", [{}])[0].get("text", "{}")
        data = json.loads(text_data)
        claims = data.get("claims", [])
        print(f"[{self.name}] OK Recalled {len(claims)} claim(s) (total matched: {data.get('total_matched', 0)}):")
        for i, c in enumerate(claims, 1):
            print(f"    [{i}] (score={c.get('score', 0):.2f}, writer={c.get('writer_label')}, ns={c.get('namespace_slug')})")
            print(f"        \"{c.get('content')}\"")
        return data

    def close(self):
        self.running = False
        if self.sse_resp:
            try:
                self.sse_resp.close()
            except Exception:
                pass

def main():
    parser = argparse.ArgumentParser(description="GAMS MCP Client Agent")
    parser.add_argument("--name", default="Agent-Alpha", help="Agent identifier")
    parser.add_argument("--url", default="http://127.0.0.1:8080", help="GAMS base URL")
    parser.add_argument("--key", required=True, help="Agent API key")
    parser.add_argument("--remember", help="Claim text to remember")
    parser.add_argument("--confidence", type=float, default=0.9, help="Confidence [0.0 - 1.0]")
    parser.add_argument("--evidence-uri", default="agent://observation", help="Evidence URI")
    parser.add_argument("--evidence-desc", default="Direct agent observation", help="Evidence description")
    parser.add_argument("--namespace", default="default", help="Target namespace")
    parser.add_argument("--tags", nargs="*", default=[], help="Tags for the claim")
    parser.add_argument("--recall", help="Query to recall")
    parser.add_argument("--sleep-after-write", type=float, default=0.0, help="Seconds to sleep after remember")

    args = parser.parse_args()

    agent = MCPAgent(args.name, args.url, args.key)
    try:
        agent.connect()

        if args.remember:
            agent.remember(
                content=args.remember,
                confidence=args.confidence,
                evidence_uri=args.evidence_uri,
                evidence_desc=args.evidence_desc,
                namespace=args.namespace,
                tags=args.tags
            )

        if args.sleep_after_write > 0:
            print(f"[{args.name}] Waiting {args.sleep_after_write}s before recall...")
            time.sleep(args.sleep_after_write)

        if args.recall:
            agent.recall(args.recall, namespace=args.namespace)

    finally:
        agent.close()

if __name__ == "__main__":
    main()
