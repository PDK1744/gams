"""
Exponential backoff retry with jitter for handling HTTP 409 Conflict.
Aligned with GAMS optimistic concurrency strategy.
"""

import asyncio
import random
from typing import Awaitable, Callable, Optional, TypeVar

from .errors import ConflictError
from .types import RetryPolicy

T = TypeVar("T")

DEFAULT_RETRY_POLICY = RetryPolicy(
    max_attempts=3,
    base_delay_ms=100.0,
    max_delay_ms=2000.0,
)


def calculate_backoff(attempt: int, base_delay_ms: float, max_delay_ms: float) -> float:
    """Calculate exponential backoff with full jitter in seconds."""
    exp = min(attempt, 6)
    factor = 2 ** exp
    delay = (base_delay_ms / 1000.0) * factor
    # Add 10-25% random jitter to avoid thundering herd contention
    jitter = delay * (0.1 + random.random() * 0.15)
    return min(delay + jitter, max_delay_ms / 1000.0)


async def with_conflict_retry(
    operation: Callable[[int], Awaitable[T]],
    policy: Optional[RetryPolicy] = None,
) -> T:
    """Execute operation with automatic retry on ConflictError (HTTP 409)."""
    pol = policy or DEFAULT_RETRY_POLICY
    last_error: Optional[Exception] = None

    for attempt in range(pol.max_attempts):
        try:
            return await operation(attempt)
        except ConflictError as err:
            last_error = err
            if attempt == pol.max_attempts - 1:
                raise
            delay = calculate_backoff(attempt, pol.base_delay_ms, pol.max_delay_ms)
            await asyncio.sleep(delay)
        except Exception:
            raise

    if last_error:
        raise last_error
    raise RuntimeError("Retry loop exhausted without result")
