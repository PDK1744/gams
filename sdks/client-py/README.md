# gams-client

Official Python SDK for **GAMS** (Governed Agent Memory Server) — persistent, verifiable, governed memory infrastructure for AI agents.

## Features

- **Asyncio-First Architecture**: `AsyncGAMSClient` is optimized for modern async agent runtimes and frameworks (LangChain, LlamaIndex, PydanticAI).
- **Synchronous Ergonomics**: `GAMSClient` provides an identical sync interface for automation scripts and CLI tooling via clean delegation.
- **Pydantic v2 Models**: Ingress validation, `.model_dump()` support, and typed integration with LLM agent frameworks.
- **Optimistic Concurrency & Backoff**: Automatic retry with exponential jittered backoff on 409 version conflicts.
- **Lineage & Governance**: Inspect full version history, linked evidence, and immutable audit trails.

## Installation

```bash
pip install gams-client
```

## Quickstart

### Async Example (`AsyncGAMSClient`)

```python
import asyncio
from gams_client import AsyncGAMSClient, EvidenceItem

async def main():
    async with AsyncGAMSClient(
        api_key="gams_tenant_...",
        base_url="http://127.0.0.1:8080"
    ) as client:
        # Ensure working namespace exists
        await client.ensure_namespace("engineering-decisions")

        # Remember a claim with verifiable evidence
        res = await client.remember(
            content="PostgreSQL WAL selected for high concurrency transactions",
            confidence=0.95,
            namespace="engineering-decisions",
            evidence=[
                EvidenceItem(
                    uri="doc://adrs/004-storage-engine.md",
                    description="Architecture Decision Record 004",
                    evidence_type="file"
                )
            ]
        )
        print(f"Stored claim {res.claim_id} (version {res.version})")

        # Recall memories with hybrid scoring (BM25 + Confidence + Recency)
        results = await client.recall(
            query="storage engine choice",
            namespace="engineering-decisions"
        )
        for claim in results.claims:
            print(f"[{claim.score:.2f}] {claim.content}")

        # Inspect complete lineage and governance audit trail
        lineage = await client.inspect(res.claim_id)
        print(f"Versions: {len(lineage.versions)}, Audit Events: {len(lineage.audit)}")

asyncio.run(main())
```

### Synchronous Example (`GAMSClient`)

```python
from gams_client import GAMSClient, EvidenceItem

client = GAMSClient(api_key="gams_tenant_...", base_url="http://127.0.0.1:8080")

# Store memory synchronously
result = client.remember(
    content="Production deployment requires TLS 1.3",
    confidence=0.99,
    evidence=[EvidenceItem(uri="doc://security-baseline", description="Security policy")]
)

# Recall memory
recall_res = client.recall(query="TLS requirement")
print(recall_res.claims[0].content)

# Inspect lineage
lineage = client.inspect(result.claim_id)
print(f"Claim status: {lineage.claim.status}")

client.close()
```

## License

Apache-2.0
