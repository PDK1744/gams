"""
Custom exception hierarchy for gams-client.
Maps HTTP status codes to specialized, actionable errors.
"""

from typing import Any, Optional


class GAMSClientError(Exception):
    """Base exception for all GAMS client errors."""

    def __init__(
        self,
        message: str,
        status_code: Optional[int] = None,
        details: Optional[Any] = None,
    ) -> None:
        super().__init__(message)
        self.message = message
        self.status_code = status_code
        self.details = details

    def __str__(self) -> str:
        if self.status_code:
            return f"[{self.status_code}] {self.message}"
        return self.message


class ValidationError(GAMSClientError):
    """Raised on HTTP 400 Bad Request or client-side input validation errors."""

    def __init__(self, message: str, details: Optional[Any] = None) -> None:
        super().__init__(message, status_code=400, details=details)


class AuthenticationError(GAMSClientError):
    """Raised on HTTP 401 Unauthorized (missing, invalid, or expired API key)."""

    def __init__(self, message: str, details: Optional[Any] = None) -> None:
        super().__init__(message, status_code=401, details=details)


class ForbiddenError(GAMSClientError):
    """Raised on HTTP 403 Forbidden (namespace access denied or key restricted)."""

    def __init__(self, message: str, details: Optional[Any] = None) -> None:
        super().__init__(message, status_code=403, details=details)


class NotFoundError(GAMSClientError):
    """Raised on HTTP 404 Not Found (missing claim or namespace)."""

    def __init__(self, message: str, details: Optional[Any] = None) -> None:
        super().__init__(message, status_code=404, details=details)


class ConflictError(GAMSClientError):
    """Raised on HTTP 409 Conflict (e.g. optimistic concurrency version mismatch)."""

    def __init__(self, message: str, details: Optional[Any] = None) -> None:
        super().__init__(message, status_code=409, details=details)
