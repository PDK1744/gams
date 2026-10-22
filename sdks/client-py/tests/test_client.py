import json
import pytest
import httpx

from gams_client import AsyncGAMSClient, GAMSClient
from gams_client.errors import (
    AuthenticationError,
    ConflictError,
    ForbiddenError,
    NotFoundError,
    ValidationError,
)
from gams_client.types import EvidenceItem, RememberInput


def test_client_init_validation():
    with pytest.raises(ValidationError):
        AsyncGAMSClient(api_key="")

    with pytest.raises(ValidationError):
        GAMSClient(api_key="")


@pytest.mark.asyncio
async def test_async_client_error_mapping():
    def handler(request: httpx.Request):
        assert request.headers.get("authorization") == "Bearer test-key"
        if request.url.path == "/v1/claims":
            try:
                body = json.loads(request.content)
                if body.get("claim_id") == "conflict-id":
                    return httpx.Response(409, json={"error": "version conflict"})
            except Exception:
                pass
            return httpx.Response(400, json={"error": "invalid content"})
        elif request.url.path == "/v1/recall":
            return httpx.Response(401, json={"error": "unauthorized"})
        elif request.url.path == "/v1/namespaces/forbidden-ns":
            return httpx.Response(403, json={"error": "access denied"})
        elif request.url.path == "/v1/claims/missing-id":
            return httpx.Response(404, json={"error": "not found"})
        elif request.url.path == "/v1/claims/conflict-id":
            return httpx.Response(409, json={"error": "version conflict"})
        return httpx.Response(500, json={"error": "internal error"})

    mock_client = httpx.AsyncClient(transport=httpx.MockTransport(handler), base_url="http://test")
    client = AsyncGAMSClient(api_key="test-key", http_client=mock_client)

    # 400
    with pytest.raises(ValidationError) as exc:
        await client.remember(content="fail", confidence=0.5, evidence=[])
    assert "invalid content" in str(exc.value)

    # 401
    with pytest.raises(AuthenticationError):
        await client.recall(query="test")

    # 403
    with pytest.raises(ForbiddenError):
        await client.ensure_namespace("forbidden-ns")

    # 404
    with pytest.raises(NotFoundError):
        await client.inspect("missing-id")

    # 409
    with pytest.raises(ConflictError):
        await client.remember(
            content="conflict test",
            confidence=0.9,
            claim_id="conflict-id",
            retry_on_conflict=False,
        )


@pytest.mark.asyncio
async def test_async_client_methods():
    remember_called = False
    recall_called = False
    forget_called = False
    inspect_called = False
    list_ns_called = False
    ensure_ns_called = False

    def handler(request: httpx.Request):
        nonlocal remember_called, recall_called, forget_called, inspect_called, list_ns_called, ensure_ns_called
        path = request.url.path

        if request.method == "POST" and path == "/v1/claims":
            remember_called = True
            body = json.loads(request.content)
            assert body["content"] == "Valid memory"
            return httpx.Response(
                201,
                json={
                    "claim_id": "c-001",
                    "version": 1,
                    "created_at": "2026-10-22T00:00:00Z",
                    "status": "created",
                },
            )

        elif request.method == "POST" and path == "/v1/recall":
            recall_called = True
            return httpx.Response(
                200,
                json={
                    "claims": [
                        {
                            "claim_id": "c-001",
                            "content": "Valid memory",
                            "confidence": 0.95,
                            "version": 1,
                            "status": "active",
                            "score": 0.92,
                            "created_at": "2026-10-22T00:00:00Z",
                            "updated_at": "2026-10-22T00:00:00Z",
                            "evidence": [],
                        }
                    ],
                    "total_matched": 1,
                    "query_ms": 2.5,
                },
            )

        elif request.method == "POST" and path == "/v1/claims/c-001/forget":
            forget_called = True
            body = json.loads(request.content)
            assert body["reason"] == "obsolete"
            return httpx.Response(
                200,
                json={"claim_id": "c-001", "forgotten_at": "2026-10-22T01:00:00Z"},
            )

        elif request.method == "GET" and path == "/v1/claims/c-001":
            inspect_called = True
            return httpx.Response(
                200,
                json={
                    "claim": {
                        "id": "c-001",
                        "tenant_id": "t-1",
                        "namespace_id": "ns-1",
                        "current_version": 1,
                        "content": "Valid memory",
                        "content_hash": "hash",
                        "confidence": 0.95,
                        "status": "active",
                        "created_at": "2026-10-22T00:00:00Z",
                        "updated_at": "2026-10-22T00:00:00Z",
                    },
                    "versions": [
                        {
                            "version": 1,
                            "content": "Valid memory",
                            "confidence": 0.95,
                            "written_at": "2026-10-22T00:00:00Z",
                            "contradicts": [],
                        }
                    ],
                    "evidence": [],
                    "audit": [],
                },
            )

        elif request.method == "GET" and path == "/v1/namespaces":
            list_ns_called = True
            return httpx.Response(
                200,
                json=[
                    {
                        "id": "ns-1",
                        "tenant_id": "t-1",
                        "slug": "default",
                        "name": "Default",
                        "status": "active",
                        "access_mode": "tenant_wide",
                        "created_at": "2026-10-22T00:00:00Z",
                        "updated_at": "2026-10-22T00:00:00Z",
                    }
                ],
            )

        elif request.method == "GET" and path == "/v1/namespaces/new-ns":
            return httpx.Response(404, json={"error": "not found"})

        elif request.method == "POST" and path == "/v1/namespaces":
            ensure_ns_called = True
            body = json.loads(request.content)
            return httpx.Response(
                201,
                json={
                    "id": "ns-new",
                    "tenant_id": "t-1",
                    "slug": body["slug"],
                    "name": body["name"],
                    "status": "active",
                    "access_mode": "tenant_wide",
                    "created_at": "2026-10-22T00:00:00Z",
                    "updated_at": "2026-10-22T00:00:00Z",
                },
            )

        return httpx.Response(404, json={"error": "unknown route"})

    mock_client = httpx.AsyncClient(transport=httpx.MockTransport(handler), base_url="http://test")
    client = AsyncGAMSClient(api_key="test-key", http_client=mock_client)

    # 1. remember
    rem = await client.remember(
        content="Valid memory",
        confidence=0.95,
        evidence=[EvidenceItem(uri="doc://test", description="Test item")],
    )
    assert rem.claim_id == "c-001"
    assert rem.version == 1
    assert remember_called

    # 2. recall
    rec = await client.recall(query="Valid")
    assert rec.total_matched == 1
    assert rec.claims[0].claim_id == "c-001"
    assert recall_called

    # 3. inspect
    insp = await client.inspect("c-001")
    assert insp.claim.id == "c-001"
    assert len(insp.versions) == 1
    assert inspect_called

    # 4. forget
    forg = await client.forget("c-001", "obsolete")
    assert forg.claim_id == "c-001"
    assert forget_called

    # 5. list_namespaces
    namespaces = await client.list_namespaces()
    assert len(namespaces) == 1
    assert namespaces[0].slug == "default"
    assert list_ns_called

    # 6. ensure_namespace
    ns = await client.ensure_namespace("new-ns", name="New Space")
    assert ns.slug == "new-ns"
    assert ensure_ns_called


def test_sync_client_running_loop_guard():
    client = GAMSClient(api_key="test-key")
    try:
        async def run_inside_loop():
            with pytest.raises(RuntimeError) as exc:
                client.recall(query="test")
            assert "cannot be used from inside an active asyncio event loop" in str(exc.value)

        import asyncio
        asyncio.run(run_inside_loop())
    finally:
        client.close()


@pytest.mark.asyncio
async def test_client_request_signing():
    captured_headers = {}

    def handler(request: httpx.Request):
        nonlocal captured_headers
        captured_headers = dict(request.headers)
        return httpx.Response(200, json={"claims": [], "total_matched": 0})

    mock_client = httpx.AsyncClient(transport=httpx.MockTransport(handler), base_url="http://test")
    client = AsyncGAMSClient(
        api_key="secret-token-456",
        http_client=mock_client,
        sign_requests=True,
    )

    await client.recall(query="test-query")
    assert "x-gams-timestamp" in captured_headers
    assert "x-gams-signature" in captured_headers
    assert len(captured_headers["x-gams-signature"]) == 64

