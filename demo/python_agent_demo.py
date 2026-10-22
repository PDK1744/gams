#!/usr/bin/env python3
"""
GAMS Phase 4 Demo: Python Agent with Lineage Governance & Contradiction Protocol.

Demonstrates:
1. AsyncGAMSClient storing an initial architecture decision with verifiable evidence.
2. Contradicting that decision when requirements change, linking back to the original claim.
3. Using inspect() to display complete governance lineage, version history, and audit trail.
4. Using GAMSClient (sync wrapper) to inspect the same state, showing dual ergonomic patterns.
"""

import asyncio
import os
import sys

from gams_client import AsyncGAMSClient, GAMSClient, EvidenceItem


def print_banner(text: str) -> None:
    print(f"\n{'=' * 80}")
    print(f" {text}")
    print(f"{'=' * 80}")


def print_section(text: str) -> None:
    print(f"\n{'-' * 80}")
    print(f" {text}")
    print(f"{'-' * 80}")


async def run_async_governance_workflow(api_key: str, base_url: str):
    print_banner("1. ASYNC WORKFLOW: AsyncGAMSClient (Agent Frameworks / Async Runtimes)")

    async with AsyncGAMSClient(api_key=api_key, base_url=base_url) as client:
        # Step 1: Ensure governance namespace exists
        print("\n[Step 1] Ensuring namespace 'codebase-decisions' exists...")
        ns = await client.ensure_namespace(
            slug="codebase-decisions",
            name="Codebase Architecture Decisions",
            description="Persistent architectural decision records (ADRs) and justifications",
        )
        print(f"✔ Namespace confirmed: {ns.slug} (access_mode={ns.access_mode})")

        # Step 2: Store ADR-001 (Initial decision)
        print("\n[Step 2] Agent remembers Initial Decision (ADR-001)...")
        adr1 = await client.remember(
            content="Use SQLite WAL mode for storage to satisfy single-binary local-first design",
            confidence=0.88,
            namespace="codebase-decisions",
            tags=["storage", "sqlite", "adr"],
            evidence=[
                EvidenceItem(
                    uri="doc://adrs/001-storage-engine.md",
                    description="ADR-001: Evaluation of embedded databases",
                    evidence_type="file",
                )
            ],
            change_reason="Initial design baseline",
        )
        print(f"✔ ADR-001 stored: claim_id={adr1.claim_id} | version={adr1.version} | status={adr1.status}")

        # Step 3: Hybrid Recall
        print("\n[Step 3] Querying memory via hybrid recall (BM25 + Confidence + Recency)...")
        recall_res = await client.recall(
            query="storage engine single binary",
            namespace="codebase-decisions",
        )
        for c in recall_res.claims:
            print(f"  • Matched Claim [{c.claim_id}]: \"{c.content}\" (score={c.score:.2f})")

        # Step 4: Contradict ADR-001 with ADR-002
        print("\n[Step 4] Requirements changed: Agent contradicts ADR-001 with ADR-002...")
        adr2 = await client.remember(
            content="Migrated storage layer to Turso/libsql replication for distributed high availability",
            confidence=0.98,
            namespace="codebase-decisions",
            tags=["storage", "turso", "libsql", "adr"],
            contradicts=[adr1.claim_id],
            evidence=[
                EvidenceItem(
                    uri="doc://benchmarks/distributed-ha-tests.pdf",
                    description="Cluster failover and consensus latency verification",
                    evidence_type="file",
                ),
                EvidenceItem(
                    uri="https://turso.tech/docs/replication",
                    description="Libsql primary/replica sync documentation",
                    evidence_type="url",
                ),
            ],
            change_reason="ADR-002 supersedes ADR-001 following enterprise HA SLA requirements",
        )
        print(f"✔ ADR-002 stored: claim_id={adr2.claim_id} | contradicts=[{adr1.claim_id}]")

        # Step 5: Inspect original ADR-001 lineage (verifying contradiction pointer)
        print("\n[Step 5] Inspecting superseded ADR-001 to verify governance tracking...")
        insp1 = await client.inspect(adr1.claim_id)
        c1 = insp1.claim
        print(f"  Status:          [{c1.status.upper()}]")
        print(f"  Contradicted By: {c1.contradicted_by}")
        print(f"  Version count:   {len(insp1.versions)}")
        print(f"  Evidence linked: {len(insp1.evidence)} item(s)")

        # Step 6: Inspect ADR-002 full lineage and audit trail
        print("\n[Step 6] Inspecting ADR-002 full governance lineage & audit trail...")
        insp2 = await client.inspect(adr2.claim_id)
        c2 = insp2.claim

        print_section(f"LINEAGE REPORT FOR CLAIM: {c2.id}")
        print(f"Content:         {c2.content}")
        print(f"Status:          [{c2.status.upper()}] | Confidence: {c2.confidence:.2f}")
        print(f"Writer:          {c2.writer_label or c2.writer_id}")
        print(f"Created:         {c2.created_at}")

        print("\nVersions:")
        for v in insp2.versions:
            contra = f"contradicts={v.contradicts}" if v.contradicts else "none"
            print(f"  • v{v.version} | Writer: {v.writer_label or v.writer_id} | {contra}")
            print(f"    Change Reason: {v.change_reason}")

        print("\nEvidence Records:")
        for ev in insp2.evidence:
            print(f"  • [v{ev.claim_version}] [{ev.evidence_type}] {ev.uri}: {ev.description}")

        print("\nGovernance Audit Trail:")
        for a in insp2.audit:
            print(f"  • {a.occurred_at} | EVENT: {a.event_type:<10} | Actor: {a.actor_label or a.actor_key_id}")

        return adr2.claim_id


def run_sync_workflow(api_key: str, base_url: str, claim_id: str):
    print_banner("2. SYNCHRONOUS WORKFLOW: GAMSClient (Automation Scripts / CLI Tools)")

    print(f"\nConnecting via synchronous GAMSClient to inspect claim {claim_id}...")
    with GAMSClient(api_key=api_key, base_url=base_url) as sync_client:
        insp = sync_client.inspect(claim_id)
        print(f"✔ Synchronous inspect succeeded:")
        print(f"  Claim ID:   {insp.claim.id}")
        print(f"  Status:     {insp.claim.status}")
        print(f"  Confidence: {insp.claim.confidence}")
        print(f"  Versions:   {len(insp.versions)}")
        print(f"  Audit logs: {len(insp.audit)}")
        print(f"  Evidence:   {len(insp.evidence)}")

        print("\n✔ Both AsyncGAMSClient and GAMSClient share identical Pydantic v2 data models.")


def main():
    base_url = os.getenv("GAMS_SERVER_URL", "http://127.0.0.1:8080")
    api_key = os.getenv("GAMS_API_KEY", "")

    if not api_key:
        print("[!] GAMS_API_KEY environment variable not set.")
        print("    Running local bootstrap demo requires a running server or valid API key.")
        print("    Example: export GAMS_API_KEY=gams_local_... GAMS_SERVER_URL=http://127.0.0.1:8080")
        print("    Usage: python3 demo/python_agent_demo.py")
        sys.exit(1)

    print(f"Targeting GAMS Server: {base_url}")
    claim_id = asyncio.run(run_async_governance_workflow(api_key, base_url))
    run_sync_workflow(api_key, base_url, claim_id)
    print_banner("DEMO COMPLETE: Governance & Versioning UX Proven via Python SDK")


if __name__ == "__main__":
    main()
