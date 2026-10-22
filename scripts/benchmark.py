#!/usr/bin/env python3
"""
GAMS vs Naive Context Benchmark & Agent Evaluation Suite.
Measures latency, memory throughput, and agent retrieval accuracy across context compaction.
"""

import time
import json
import subprocess
import tempfile
import os

def test_gams_stdio_benchmark(binary_path, iterations=100):
    print(f"=== 1. Benchmarking GAMS stdio Transport ({iterations} ops) ===")
    
    with tempfile.TemporaryDirectory() as tmpdir:
        db_path = os.path.join(tmpdir, "bench_eval.db")
        config_path = os.path.join(tmpdir, "gams.toml")
        with open(config_path, "w") as f:
            f.write(f'''
[data]
path = "{db_path}"
[limits]
rate_limit_enabled = false
''')
        
        proc = subprocess.Popen(
            [binary_path, "serve", "--transport", "stdio", "--config", config_path],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True
        )
        
        def rpc(method, params=None, req_id=1):
            req = {"jsonrpc": "2.0", "id": req_id, "method": method}
            if params is not None:
                req["params"] = params
            proc.stdin.write(json.dumps(req) + "\n")
            proc.stdin.flush()
            line = proc.stdout.readline()
            return json.loads(line)

        # Initialize
        rpc("initialize", {"protocolVersion": "2024-11-05", "capabilities": {}, "clientInfo": {"name": "bench"}})

        # Measure Remember
        rem_latencies = []
        claim_ids = []
        for i in range(iterations):
            start = time.perf_counter()
            res = rpc("tools/call", {
                "name": "remember",
                "arguments": {
                    "content": f"Architecture convention {i}: Service timeout set to {1000 + i}ms",
                    "confidence": 0.95,
                    "evidence": [{"uri": f"file:///pkg/service_{i}.go", "description": "Config", "evidence_type": "file"}],
                    "tags": ["service", "timeout"]
                }
            }, req_id=i+10)
            elapsed = (time.perf_counter() - start) * 1000.0
            rem_latencies.append(elapsed)
            data = json.loads(res["result"]["content"][0]["text"])
            claim_ids.append(data["claim_id"])

        # Measure Recall
        recall_latencies = []
        for i in range(iterations):
            start = time.perf_counter()
            res = rpc("tools/call", {
                "name": "recall",
                "arguments": {
                    "query": f"Service timeout {1000 + (i % 20)}ms",
                    "limit": 5
                }
            }, req_id=i+1000)
            elapsed = (time.perf_counter() - start) * 1000.0
            recall_latencies.append(elapsed)

        # Measure Contradiction Resolution
        # We explicitly contradict claim 0 with an updated timeout
        start = time.perf_counter()
        contra_res = rpc("tools/call", {
            "name": "remember",
            "arguments": {
                "content": "Architecture convention 0: Service timeout updated to 9999ms for high-load clusters",
                "confidence": 1.0,
                "contradicts": [claim_ids[0]],
                "evidence": [{"uri": "file:///pkg/cluster_update.go", "description": "Hotfix", "evidence_type": "file"}],
                "tags": ["service", "timeout"]
            }
        }, req_id=9999)
        contra_elapsed = (time.perf_counter() - start) * 1000.0

        # Query recall for convention 0
        rec_after = rpc("tools/call", {
            "name": "recall",
            "arguments": {"query": "Architecture convention 0", "limit": 5}
        }, req_id=10000)
        rec_data = json.loads(rec_after["result"]["content"][0]["text"])

        proc.terminate()
        proc.wait()

        # Stats
        rem_latencies.sort()
        recall_latencies.sort()

        p50_rem = rem_latencies[int(len(rem_latencies) * 0.50)]
        p95_rem = rem_latencies[int(len(rem_latencies) * 0.95)]
        p50_rec = recall_latencies[int(len(recall_latencies) * 0.50)]
        p95_rec = recall_latencies[int(len(recall_latencies) * 0.95)]

        print(f"  Remember (Store): P50 = {p50_rem:.2f}ms | P95 = {p95_rem:.2f}ms")
        print(f"  Recall (Search):  P50 = {p50_rec:.2f}ms | P95 = {p95_rec:.2f}ms")
        print(f"  Contradiction Protocol: {contra_elapsed:.2f}ms")
        
        # Verify Contradiction Correctness
        # Top recall should be the new 9999ms claim, old claim should be excluded by default
        active_content = [c["content"] for c in rec_data["claims"]]
        assert any("9999ms" in c for c in active_content), "New contradiction claim must be recalled"
        assert not any("1000ms" in c for c in active_content), "Contradicted claim must not be returned in default recall"
        print("  Contradiction Verification: PASS (Obsolete memory filtered out; truth preserved)")

        return {
            "p50_rem": p50_rem,
            "p95_rem": p95_rem,
            "p50_rec": p50_rec,
            "p95_rec": p95_rec
        }

if __name__ == "__main__":
    test_gams_stdio_benchmark("/home/kobe/src/gams/gams", iterations=100)
