"""
Official Python client library for GAMS (Governed Agent Memory Server).
"""

from .client import AsyncGAMSClient, GAMSClient
from .errors import (
    AuthenticationError,
    ConflictError,
    ForbiddenError,
    GAMSClientError,
    NotFoundError,
    ValidationError,
)
from .types import (
    AccessMode,
    AuditEvent,
    Claim,
    ClaimVersion,
    ConflictInfo,
    EvidenceItem,
    EvidenceType,
    ForgetResult,
    InspectResult,
    Namespace,
    RecallClaim,
    RecallInput,
    RecallResult,
    RememberInput,
    RememberResult,
    RetryPolicy,
)

__version__ = "0.1.0"

__all__ = [
    "AsyncGAMSClient",
    "GAMSClient",
    "GAMSClientError",
    "ValidationError",
    "AuthenticationError",
    "ForbiddenError",
    "NotFoundError",
    "ConflictError",
    "AccessMode",
    "EvidenceItem",
    "EvidenceType",
    "RememberInput",
    "ConflictInfo",
    "RememberResult",
    "RecallInput",
    "RecallClaim",
    "RecallResult",
    "ForgetResult",
    "Claim",
    "ClaimVersion",
    "AuditEvent",
    "InspectResult",
    "Namespace",
    "RetryPolicy",
]
