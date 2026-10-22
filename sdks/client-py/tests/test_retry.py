import pytest

from gams_client.errors import ConflictError, ValidationError
from gams_client.retry import calculate_backoff, with_conflict_retry
from gams_client.types import RetryPolicy


def test_calculate_backoff_bounds():
    base = 100.0
    max_d = 2000.0
    for attempt in range(5):
        delay = calculate_backoff(attempt, base, max_d)
        assert delay > 0
        assert delay <= max_d / 1000.0


@pytest.mark.asyncio
async def test_with_conflict_retry_success():
    calls = 0

    async def op(attempt: int):
        nonlocal calls
        calls += 1
        return "success"

    res = await with_conflict_retry(op)
    assert res == "success"
    assert calls == 1


@pytest.mark.asyncio
async def test_with_conflict_retry_retries_on_conflict():
    attempts = 0

    async def op(attempt: int):
        nonlocal attempts
        attempts += 1
        if attempt < 2:
            raise ConflictError("Version mismatch")
        return "recovered"

    policy = RetryPolicy(max_attempts=3, base_delay_ms=1.0, max_delay_ms=10.0)
    res = await with_conflict_retry(op, policy)
    assert res == "recovered"
    assert attempts == 3


@pytest.mark.asyncio
async def test_with_conflict_retry_non_conflict_raises_immediately():
    attempts = 0

    async def op(attempt: int):
        nonlocal attempts
        attempts += 1
        raise ValidationError("Bad request")

    with pytest.raises(ValidationError):
        await with_conflict_retry(op)
    assert attempts == 1


@pytest.mark.asyncio
async def test_with_conflict_retry_exhausted_raises():
    attempts = 0

    async def op(attempt: int):
        nonlocal attempts
        attempts += 1
        raise ConflictError("Permanent conflict")

    policy = RetryPolicy(max_attempts=2, base_delay_ms=1.0, max_delay_ms=5.0)
    with pytest.raises(ConflictError):
        await with_conflict_retry(op, policy)
    assert attempts == 2
