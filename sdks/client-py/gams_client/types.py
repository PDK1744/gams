"""
Pydantic v2 data models for GAMS (Governed Agent Memory Server).
Provides strict validation on ingress, JSON serialization (.model_dump()),
and seamless interoperability with LangChain, LlamaIndex, and PydanticAI.
"""

from typing import Any, Literal, Optional, Union
from pydantic import BaseModel, ConfigDict, Field, field_validator

EvidenceType = Literal["url", "file", "tool_output", "agent_assertion", "human"]
AccessMode = Literal["tenant_wide", "key_restricted"]


class EvidenceItem(BaseModel):
    """An individual piece of verifiable evidence grounding a claim."""

    model_config = ConfigDict(extra="ignore")

    uri: str
    description: str
    evidence_type: Union[EvidenceType, str] = "human"
    captured_at: Optional[Union[str, int]] = None
    metadata: Optional[dict[str, Any]] = None


class RememberInput(BaseModel):
    """Input payload for remembering or updating a claim."""

    model_config = ConfigDict(extra="ignore")

    content: str
    confidence: float
    evidence: list[Union[EvidenceItem, dict[str, Any]]] = Field(default_factory=list)
    contradicts: Optional[list[str]] = None
    tags: Optional[list[str]] = None
    namespace: Optional[str] = None
    claim_id: Optional[str] = None
    base_version: Optional[int] = None
    change_reason: Optional[str] = None
    metadata: Optional[dict[str, Any]] = None


class ConflictInfo(BaseModel):
    """Details returned on HTTP 409 conflict during optimistic concurrency checks."""

    model_config = ConfigDict(extra="ignore")

    current_version: int
    updated_at: Union[str, int]


class RememberResult(BaseModel):
    """Result of remembering a claim."""

    model_config = ConfigDict(extra="ignore")

    claim_id: str
    version: int
    created_at: Union[str, int]
    status: Union[Literal["created", "updated", "conflict", "idempotent"], str]
    conflict: Optional[ConflictInfo] = None


class RecallInput(BaseModel):
    """Input payload for recalling relevant claims."""

    model_config = ConfigDict(extra="ignore")

    query: str
    limit: Optional[int] = 10
    confidence_min: Optional[float] = None
    tags: Optional[list[str]] = None
    namespace: Optional[str] = None
    include_contradicted: Optional[bool] = False
    include_forgotten: Optional[bool] = False
    recency_window_days: Optional[float] = None


class RecallClaim(BaseModel):
    """A single matched claim returned by hybrid recall scoring."""

    model_config = ConfigDict(extra="ignore")

    claim_id: str
    content: str
    confidence: float
    version: int
    status: str
    score: float
    tags: list[str] = Field(default_factory=list)
    namespace_slug: Optional[str] = None
    namespace_id: Optional[str] = None
    evidence: list[EvidenceItem] = Field(default_factory=list)
    writer_label: Optional[str] = None
    created_at: Union[str, int]
    updated_at: Union[str, int]

    @field_validator("tags", mode="before")
    @classmethod
    def normalize_tags(cls, v: Any) -> list[str]:
        return v if v is not None else []


class RecallResult(BaseModel):
    """Result set returned by recall."""

    model_config = ConfigDict(extra="ignore")

    claims: list[RecallClaim] = Field(default_factory=list)
    total_matched: int
    query_ms: Optional[float] = None


class ForgetResult(BaseModel):
    """Result of soft-deleting a claim."""

    model_config = ConfigDict(extra="ignore")

    claim_id: str
    forgotten_at: Union[str, int]


class Claim(BaseModel):
    """Current state of a memory claim."""

    model_config = ConfigDict(extra="ignore")

    id: str
    tenant_id: str
    namespace_id: str
    current_version: int
    content: str
    content_hash: str
    confidence: float
    status: str
    writer_id: Optional[str] = None
    writer_label: Optional[str] = None
    tags: list[str] = Field(default_factory=list)
    created_at: Union[str, int]
    updated_at: Union[str, int]
    forgotten_at: Optional[Union[str, int]] = None
    forgotten_reason: Optional[str] = None
    contradicted_by: Optional[str] = None
    metadata: Optional[dict[str, Any]] = None

    @field_validator("tags", mode="before")
    @classmethod
    def normalize_tags(cls, v: Any) -> list[str]:
        return v if v is not None else []


class ClaimVersion(BaseModel):
    """Point-in-time snapshot of a claim's version history."""

    model_config = ConfigDict(extra="ignore")

    id: Optional[str] = None
    claim_id: Optional[str] = None
    tenant_id: Optional[str] = None
    namespace_id: Optional[str] = None
    version: int
    content: str
    confidence: float
    writer_id: Optional[str] = None
    writer_label: Optional[str] = None
    written_at: Union[str, int]
    change_reason: Optional[str] = None
    contradicts: list[str] = Field(default_factory=list)


class AuditEvent(BaseModel):
    """Immutable audit log event associated with memory operations."""

    model_config = ConfigDict(extra="ignore")

    id: Optional[str] = None
    tenant_id: Optional[str] = None
    namespace_id: Optional[str] = None
    event_type: str
    actor_key_id: Optional[str] = None
    actor_label: Optional[str] = None
    claim_id: Optional[str] = None
    claim_version: Optional[int] = None
    payload: Optional[dict[str, Any]] = None
    occurred_at: Union[str, int]
    server_version: Optional[str] = None


class InspectResult(BaseModel):
    """Full lineage, version history, evidence, and audit trail for a claim."""

    model_config = ConfigDict(extra="ignore")

    claim: Claim
    versions: list[ClaimVersion] = Field(default_factory=list)
    evidence: list[EvidenceItem] = Field(default_factory=list)
    audit: list[AuditEvent] = Field(default_factory=list)


class Namespace(BaseModel):
    """First-class namespace entity with metadata and access control."""

    model_config = ConfigDict(extra="ignore")

    id: str
    tenant_id: str
    slug: str
    name: str
    description: Optional[str] = None
    status: str
    access_mode: Union[AccessMode, str] = "tenant_wide"
    created_at: Union[str, int]
    updated_at: Union[str, int]


class RetryPolicy(BaseModel):
    """Configuration for optimistic concurrency conflict backoff retries."""

    model_config = ConfigDict(extra="ignore")

    max_attempts: int = 3
    base_delay_ms: float = 100.0
    max_delay_ms: float = 2000.0
