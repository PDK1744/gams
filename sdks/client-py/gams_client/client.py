"""
Client implementation for GAMS (Governed Agent Memory Server).
Provides AsyncGAMSClient (asyncio-first) and GAMSClient (sync wrapper).
"""

import asyncio
import threading
from typing import Any, List, Optional, Union
import urllib.parse

import httpx

from .errors import (
    AuthenticationError,
    ConflictError,
    ForbiddenError,
    GAMSClientError,
    NotFoundError,
    ValidationError,
)
from .retry import with_conflict_retry
from .types import (
    EvidenceItem,
    ForgetResult,
    InspectResult,
    Namespace,
    RecallInput,
    RecallResult,
    RememberInput,
    RememberResult,
    RetryPolicy,
)


class AsyncGAMSClient:
    """Primary asynchronous client for GAMS REST API."""

    def __init__(
        self,
        api_key: str,
        base_url: str = "http://127.0.0.1:8080",
        timeout: float = 30.0,
        retry_policy: Optional[RetryPolicy] = None,
        http_client: Optional[httpx.AsyncClient] = None,
    ) -> None:
        if not api_key:
            raise ValidationError("AsyncGAMSClient requires a non-empty api_key")

        self.api_key = api_key
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self.retry_policy = retry_policy

        self._owns_client = http_client is None
        self._client = http_client or httpx.AsyncClient(
            base_url=self.base_url,
            timeout=self.timeout,
        )

    async def _request(
        self,
        method: str,
        path: str,
        json_body: Optional[Any] = None,
    ) -> Any:
        url = f"/{path.lstrip('/')}"
        headers = {
            "Authorization": f"Bearer {self.api_key}",
            "Accept": "application/json",
        }
        if json_body is not None:
            headers["Content-Type"] = "application/json"

        try:
            response = await self._client.request(
                method=method,
                url=url,
                json=json_body,
                headers=headers,
            )
        except httpx.TimeoutException as err:
            raise GAMSClientError(f"Request timeout after {self.timeout}s") from err
        except httpx.RequestError as err:
            raise GAMSClientError(f"Network error: {err}") from err

        content_type = response.headers.get("content-type", "")
        data: Any = None
        if "application/json" in content_type:
            try:
                data = response.json()
            except Exception:
                data = None
        else:
            data = response.text

        if response.is_error:
            message: str = ""
            if isinstance(data, dict) and "error" in data:
                message = str(data["error"])
            elif isinstance(data, str) and data.strip():
                message = data.strip()
            else:
                message = f"HTTP {response.status_code}: {response.reason_phrase}"

            if response.status_code == 400:
                raise ValidationError(message, details=data)
            elif response.status_code == 401:
                raise AuthenticationError(message, details=data)
            elif response.status_code == 403:
                raise ForbiddenError(message, details=data)
            elif response.status_code == 404:
                raise NotFoundError(message, details=data)
            elif response.status_code == 409:
                raise ConflictError(message, details=data)
            else:
                raise GAMSClientError(message, status_code=response.status_code, details=data)

        return data

    async def remember(
        self,
        input: Optional[Union[RememberInput, dict[str, Any]]] = None,
        *,
        content: Optional[str] = None,
        confidence: Optional[float] = None,
        evidence: Optional[list[Union[EvidenceItem, dict[str, Any]]]] = None,
        contradicts: Optional[list[str]] = None,
        tags: Optional[list[str]] = None,
        namespace: Optional[str] = None,
        claim_id: Optional[str] = None,
        base_version: Optional[int] = None,
        change_reason: Optional[str] = None,
        metadata: Optional[dict[str, Any]] = None,
        retry_on_conflict: bool = True,
    ) -> RememberResult:
        """
        Store or update a claim in memory.
        If base_version is specified and an optimistic concurrency 409 conflict occurs,
        automatically retries with exponential backoff if retry_on_conflict is True.
        """
        if input is not None:
            if isinstance(input, RememberInput):
                model_input = input
            elif isinstance(input, dict):
                model_input = RememberInput(**input)
            else:
                raise ValidationError("input must be RememberInput or dict")
        else:
            if content is None or confidence is None:
                raise ValidationError("remember requires content and confidence")
            model_input = RememberInput(
                content=content,
                confidence=confidence,
                evidence=evidence or [],
                contradicts=contradicts,
                tags=tags,
                namespace=namespace,
                claim_id=claim_id,
                base_version=base_version,
                change_reason=change_reason,
                metadata=metadata,
            )

        async def _execute(attempt: int) -> RememberResult:
            if attempt > 0 and model_input.claim_id:
                try:
                    inspected = await self.inspect(model_input.claim_id)
                    model_input.base_version = inspected.claim.current_version
                except Exception:
                    pass

            payload = model_input.model_dump(exclude_none=True)
            data = await self._request("POST", "/v1/claims", json_body=payload)
            return RememberResult(**data)

        if retry_on_conflict and model_input.claim_id:
            return await with_conflict_retry(_execute, self.retry_policy)

        return await _execute(0)

    async def recall(
        self,
        input: Optional[Union[RecallInput, dict[str, Any]]] = None,
        *,
        query: Optional[str] = None,
        limit: Optional[int] = 10,
        confidence_min: Optional[float] = None,
        tags: Optional[list[str]] = None,
        namespace: Optional[str] = None,
        include_contradicted: Optional[bool] = False,
        include_forgotten: Optional[bool] = False,
        recency_window_days: Optional[float] = None,
    ) -> RecallResult:
        """Recall relevant claims using hybrid text relevance, confidence, and recency scoring."""
        if input is not None:
            if isinstance(input, RecallInput):
                model_input = input
            elif isinstance(input, dict):
                model_input = RecallInput(**input)
            else:
                raise ValidationError("input must be RecallInput or dict")
        else:
            if query is None:
                raise ValidationError("recall requires a non-empty query")
            model_input = RecallInput(
                query=query,
                limit=limit,
                confidence_min=confidence_min,
                tags=tags,
                namespace=namespace,
                include_contradicted=include_contradicted,
                include_forgotten=include_forgotten,
                recency_window_days=recency_window_days,
            )

        payload = model_input.model_dump(exclude_none=True)
        data = await self._request("POST", "/v1/recall", json_body=payload)
        return RecallResult(**data)

    async def forget(self, claim_id: str, reason: str) -> ForgetResult:
        """Mark a claim as forgotten (soft delete with permanent audit trail retention)."""
        if not claim_id:
            raise ValidationError("claim_id is required")
        if not reason:
            raise ValidationError("reason is required")

        encoded_id = urllib.parse.quote(claim_id, safe="")
        data = await self._request(
            "POST",
            f"/v1/claims/{encoded_id}/forget",
            json_body={"reason": reason},
        )
        return ForgetResult(**data)

    async def inspect(self, claim_id: str) -> InspectResult:
        """Retrieve full governance history, versions, evidence, and audit trail for a claim."""
        if not claim_id:
            raise ValidationError("claim_id is required")

        encoded_id = urllib.parse.quote(claim_id, safe="")
        data = await self._request("GET", f"/v1/claims/{encoded_id}")
        return InspectResult(**data)

    async def list_namespaces(self) -> List[Namespace]:
        """List namespaces accessible to the current API key."""
        data = await self._request("GET", "/v1/namespaces")
        if not isinstance(data, list):
            return []
        return [Namespace(**item) for item in data]

    async def ensure_namespace(
        self,
        slug: str,
        name: Optional[str] = None,
        description: Optional[str] = None,
        access_mode: str = "tenant_wide",
    ) -> Namespace:
        """Idempotently retrieve an existing namespace or create it if not present."""
        if not slug:
            raise ValidationError("slug is required")

        encoded_slug = urllib.parse.quote(slug, safe="")

        # 1. Check if namespace exists
        try:
            data = await self._request("GET", f"/v1/namespaces/{encoded_slug}")
            return Namespace(**data)
        except NotFoundError:
            pass

        # 2. Create namespace
        payload = {
            "slug": slug,
            "name": name or slug,
            "description": description,
            "access_mode": access_mode,
        }
        try:
            data = await self._request("POST", "/v1/namespaces", json_body=payload)
            return Namespace(**data)
        except ConflictError:
            # Concurrently created by another process; fetch existing
            data = await self._request("GET", f"/v1/namespaces/{encoded_slug}")
            return Namespace(**data)

    async def close(self) -> None:
        """Close the underlying HTTP client session."""
        if self._owns_client:
            await self._client.aclose()

    async def __aenter__(self) -> "AsyncGAMSClient":
        return self

    async def __aexit__(self, exc_type: Any, exc_val: Any, exc_tb: Any) -> None:
        await self.close()


class _LoopThread:
    """Dedicated background event loop runner for synchronous wrapper delegation."""

    def __init__(self) -> None:
        self._loop = asyncio.new_event_loop()
        self._thread = threading.Thread(target=self._run, daemon=True)
        self._thread.start()

    def _run(self) -> None:
        asyncio.set_event_loop(self._loop)
        self._loop.run_forever()

    def run(self, coro: Any) -> Any:
        fut = asyncio.run_coroutine_threadsafe(coro, self._loop)
        return fut.result()

    def stop(self) -> None:
        if self._loop.is_running():
            self._loop.call_soon_threadsafe(self._loop.stop)
            self._thread.join(timeout=2.0)
        if not self._loop.is_closed():
            self._loop.close()


class GAMSClient:
    """
    Synchronous wrapper for GAMS REST API.
    Delegates to AsyncGAMSClient on a managed background runner.

    Important: Not safe to use from inside an existing running asyncio event loop;
    use AsyncGAMSClient in async environments.
    """

    def __init__(
        self,
        api_key: str,
        base_url: str = "http://127.0.0.1:8080",
        timeout: float = 30.0,
        retry_policy: Optional[RetryPolicy] = None,
    ) -> None:
        if not api_key:
            raise ValidationError("GAMSClient requires a non-empty api_key")

        self._loop_thread = _LoopThread()

        async def _init_client():
            return AsyncGAMSClient(
                api_key=api_key,
                base_url=base_url,
                timeout=timeout,
                retry_policy=retry_policy,
            )

        self._async_client = self._loop_thread.run(_init_client())

    def _run(self, coro: Any) -> Any:
        try:
            loop = asyncio.get_running_loop()
        except RuntimeError:
            loop = None

        if loop is not None and loop.is_running():
            if asyncio.iscoroutine(coro):
                coro.close()
            raise RuntimeError(
                "GAMSClient (sync wrapper) cannot be used from inside an active asyncio event loop. "
                "Use AsyncGAMSClient instead in async environments."
            )
        return self._loop_thread.run(coro)

    def remember(
        self,
        input: Optional[Union[RememberInput, dict[str, Any]]] = None,
        **kwargs: Any,
    ) -> RememberResult:
        """Store or update a claim synchronously."""
        return self._run(self._async_client.remember(input, **kwargs))

    def recall(
        self,
        input: Optional[Union[RecallInput, dict[str, Any]]] = None,
        **kwargs: Any,
    ) -> RecallResult:
        """Recall relevant claims synchronously."""
        return self._run(self._async_client.recall(input, **kwargs))

    def forget(self, claim_id: str, reason: str) -> ForgetResult:
        """Mark a claim as forgotten synchronously."""
        return self._run(self._async_client.forget(claim_id, reason))

    def inspect(self, claim_id: str) -> InspectResult:
        """Inspect a claim's full lineage, history, and evidence synchronously."""
        return self._run(self._async_client.inspect(claim_id))

    def list_namespaces(self) -> List[Namespace]:
        """List accessible namespaces synchronously."""
        return self._run(self._async_client.list_namespaces())

    def ensure_namespace(
        self,
        slug: str,
        name: Optional[str] = None,
        description: Optional[str] = None,
        access_mode: str = "tenant_wide",
    ) -> Namespace:
        """Idempotently ensure a namespace exists synchronously."""
        return self._run(
            self._async_client.ensure_namespace(
                slug=slug,
                name=name,
                description=description,
                access_mode=access_mode,
            )
        )

    def close(self) -> None:
        """Close the underlying client session and runner."""
        if hasattr(self, "_loop_thread") and not self._loop_thread._loop.is_closed():
            try:
                self._loop_thread.run(self._async_client.close())
            except Exception:
                pass
            finally:
                self._loop_thread.stop()

    def __enter__(self) -> "GAMSClient":
        return self

    def __exit__(self, exc_type: Any, exc_val: Any, exc_tb: Any) -> None:
        self.close()

    def __del__(self) -> None:
        try:
            self.close()
        except Exception:
            pass
