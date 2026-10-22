import pytest
from pydantic import ValidationError as PydanticValidationError

from gams_client.types import (
    AuditEvent,
    Claim,
    ClaimVersion,
    EvidenceItem,
    InspectResult,
    Namespace,
    RecallClaim,
    RecallResult,
    RememberInput,
    RememberResult,
    RetryPolicy,
)


def test_evidence_item_defaults():
    item = EvidenceItem(uri="doc://test", description="Test description")
    assert item.uri == "doc://test"
    assert item.description == "Test description"
    assert item.evidence_type == "human"
    assert item.captured_at is None
    assert item.metadata is None


def test_remember_input_validation():
    inp = RememberInput(
        content="Test content",
        confidence=0.9,
        evidence=[{"uri": "doc://rfc", "description": "RFC doc", "evidence_type": "file"}],
        tags=["tag1", "tag2"],
    )
    dumped = inp.model_dump(exclude_none=True)
    assert dumped["content"] == "Test content"
    assert dumped["confidence"] == 0.9
    assert len(dumped["evidence"]) == 1
    assert dumped["evidence"][0]["uri"] == "doc://rfc"

    with pytest.raises(PydanticValidationError):
        RememberInput(content="Missing confidence")  # type: ignore


def test_inspect_result_parsing():
    raw_data = {
        "claim": {
            "id": "claim-123",
            "tenant_id": "tenant-456",
            "namespace_id": "ns-789",
            "current_version": 2,
            "content": "Updated database choice",
            "content_hash": "hash123",
            "confidence": 0.95,
            "status": "active",
            "created_at": "2026-10-22T00:00:00Z",
            "updated_at": "2026-10-22T01:00:00Z",
            "writer_label": "agent-alpha",
            "tags": ["db", "architecture"],
        },
        "versions": [
            {
                "version": 1,
                "content": "Initial DB choice",
                "confidence": 0.8,
                "writer_label": "agent-alpha",
                "written_at": "2026-10-22T00:00:00Z",
                "contradicts": [],
            },
            {
                "version": 2,
                "content": "Updated database choice",
                "confidence": 0.95,
                "writer_label": "agent-beta",
                "written_at": "2026-10-22T01:00:00Z",
                "change_reason": "Benchmarking results",
                "contradicts": ["old-claim-001"],
            },
        ],
        "evidence": [
            {
                "uri": "https://example.com/benchmarks",
                "description": "Benchmark chart",
                "evidence_type": "url",
            }
        ],
        "audit": [
            {
                "event_type": "WRITE",
                "actor_label": "agent-alpha",
                "occurred_at": 1792630000000,
                "payload": {"version": 1},
            }
        ],
        # Extra server field should be ignored safely
        "future_server_extension": {"foo": "bar"},
    }

    res = InspectResult(**raw_data)
    assert res.claim.id == "claim-123"
    assert res.claim.current_version == 2
    assert len(res.versions) == 2
    assert res.versions[1].contradicts == ["old-claim-001"]
    assert len(res.evidence) == 1
    assert len(res.audit) == 1
    assert res.audit[0].event_type == "WRITE"


def test_recall_result_parsing():
    raw = {
        "claims": [
            {
                "claim_id": "c-1",
                "content": "Test claim",
                "confidence": 0.9,
                "version": 1,
                "status": "active",
                "score": 0.85,
                "tags": ["a", "b"],
                "created_at": "2026-10-22T00:00:00Z",
                "updated_at": "2026-10-22T00:00:00Z",
                "evidence": [],
            }
        ],
        "total_matched": 1,
        "query_ms": 1.25,
    }
    rec = RecallResult(**raw)
    assert rec.total_matched == 1
    assert len(rec.claims) == 1
    assert rec.claims[0].score == 0.85


def test_recall_claim_handles_null_tags():
    raw = {
        "claims": [
            {
                "claim_id": "c-null-tags",
                "content": "Untagged memory",
                "confidence": 0.9,
                "version": 1,
                "status": "active",
                "score": 0.8,
                "tags": None,
                "created_at": "2026-10-22T00:00:00Z",
                "updated_at": "2026-10-22T00:00:00Z",
                "evidence": [],
            }
        ],
        "total_matched": 1,
    }
    rec = RecallResult(**raw)
    assert rec.claims[0].tags == []


def test_retry_policy_defaults():
    pol = RetryPolicy()
    assert pol.max_attempts == 3
    assert pol.base_delay_ms == 100.0
    assert pol.max_delay_ms == 2000.0
