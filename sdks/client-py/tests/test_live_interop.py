import os
import subprocess
import tempfile
import time
import pytest

from gams_client import AsyncGAMSClient, GAMSClient, EvidenceItem


@pytest.mark.asyncio
async def test_live_server_interop():
    with tempfile.TemporaryDirectory() as tmpdir:
        db_path = os.path.join(tmpdir, "gams_live_test.db")
        config_path = os.path.join(tmpdir, "gams.toml")
        port = 8899

        # Create config with dev mode / empty admin key
        config_content = f"""
[server]
http_port = {port}
admin_key = "admin-secret-key"

[data]
path = "{db_path}"

[auth]
key_cache_ttl_seconds = 300
"""
        with open(config_path, "w") as f:
            f.write(config_content)

        binary_path = os.path.abspath(os.path.join(os.path.dirname(__file__), "../../../gams"))
        if not os.path.exists(binary_path):
            pytest.skip("gams binary not built; run 'go build -o gams ./cmd/gams' first")

        # Start GAMS server
        proc = subprocess.Popen(
            [binary_path, "serve", "--config", config_path],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )

        try:
            base_url = f"http://127.0.0.1:{port}"
            # Wait for server to become healthy
            import httpx
            healthy = False
            for _ in range(50):
                try:
                    r = httpx.get(f"{base_url}/health", timeout=0.5)
                    if r.status_code == 200:
                        healthy = True
                        break
                except Exception:
                    time.sleep(0.1)

            assert healthy, "GAMS server failed to start within 5s"

            # Create a test tenant and API key via admin API
            admin_headers = {"Authorization": "Bearer admin-secret-key"}
            tenant_res = httpx.post(
                f"{base_url}/admin/tenants",
                headers=admin_headers,
                json={"name": "Python Interop Tenant", "auto_create_namespaces": True},
            )
            assert tenant_res.status_code == 201, tenant_res.text
            tenant_id = tenant_res.json()["id"]

            key_res = httpx.post(
                f"{base_url}/admin/tenants/{tenant_id}/keys",
                headers=admin_headers,
                json={"label": "python-agent"},
            )
            assert key_res.status_code == 201, key_res.text
            agent_api_key = key_res.json()["key"]

            # 1. Async client interaction
            async with AsyncGAMSClient(api_key=agent_api_key, base_url=base_url) as client:
                # Ensure namespace
                ns = await client.ensure_namespace(
                    slug="architecture-decisions",
                    name="Architecture Decisions",
                )
                assert ns.slug == "architecture-decisions"

                # Store initial decision
                claim1 = await client.remember(
                    content="We use REST exclusively for inter-service communication",
                    confidence=0.85,
                    namespace="architecture-decisions",
                    tags=["architecture", "transport"],
                    evidence=[
                        EvidenceItem(
                            uri="doc://adrs/001-rest-api.md",
                            description="Initial architecture decision",
                            evidence_type="file",
                        )
                    ],
                )
                assert claim1.claim_id
                assert claim1.version == 1

                # Recall initial decision
                recall_res = await client.recall(
                    query="inter-service communication",
                    namespace="architecture-decisions",
                )
                assert recall_res.total_matched >= 1
                assert recall_res.claims[0].claim_id == claim1.claim_id

                # Contradict initial decision with new evidence
                claim2 = await client.remember(
                    content="We migrated inter-service communication to gRPC for performance",
                    confidence=0.98,
                    namespace="architecture-decisions",
                    tags=["architecture", "transport", "grpc"],
                    contradicts=[claim1.claim_id],
                    evidence=[
                        EvidenceItem(
                            uri="doc://benchmarks/grpc-vs-rest.pdf",
                            description="Benchmark showing 4x latency reduction",
                            evidence_type="file",
                        )
                    ],
                )
                assert claim2.claim_id != claim1.claim_id

                # Inspect initial claim: must now be marked as CONTRADICTED
                inspect1 = await client.inspect(claim1.claim_id)
                assert inspect1.claim.status == "contradicted"
                assert inspect1.claim.contradicted_by == claim2.claim_id
                assert len(inspect1.versions) >= 1
                assert len(inspect1.evidence) == 1
                assert inspect1.evidence[0].uri == "doc://adrs/001-rest-api.md"

                # Inspect new claim: ACTIVE, full audit trail
                inspect2 = await client.inspect(claim2.claim_id)
                assert inspect2.claim.status == "active"
                assert len(inspect2.versions) == 1
                assert inspect2.versions[0].contradicts == [claim1.claim_id]
                assert len(inspect2.evidence) == 1
                assert len(inspect2.audit) >= 1

            # 2. Sync client interaction in a worker thread (outside pytest's running event loop)
            import concurrent.futures
            def run_sync():
                with GAMSClient(api_key=agent_api_key, base_url=base_url) as sync_client:
                    sync_inspect = sync_client.inspect(claim2.claim_id)
                    assert sync_inspect.claim.id == claim2.claim_id
                    assert sync_inspect.claim.status == "active"
                    assert len(sync_inspect.versions) == 1
                    assert sync_inspect.versions[0].contradicts == [claim1.claim_id]

            with concurrent.futures.ThreadPoolExecutor(max_workers=1) as executor:
                future = executor.submit(run_sync)
                future.result(timeout=5)

        finally:
            proc.terminate()
            try:
                proc.wait(timeout=3)
            except Exception:
                proc.kill()
