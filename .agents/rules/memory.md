# Agent Memory Protocol (GAMS)

You have access to persistent, evidence-linked memory via the GAMS (Governed Agent Memory Server) MCP tools: `remember`, `recall`, `inspect`, and `forget`.

## Proactive Autonomous Memory Guidelines

1. **Before Commencing Work / Answering Complex Questions:**
   - Proactively search memory using `recall` when approaching architectural decisions, project conventions, performance configurations, or debugging tricky components.
   - Ground reasoning in prior verified claims when relevant.

2. **After Resolving Problems / Establishing Decisions:**
   - Proactively persist key takeaways with `remember` when you:
     - Solve an elusive bug or establish a non-obvious workaround.
     - Finalize an architectural pattern, API contract, or configuration rule.
     - Identify important testing or deployment caveats.
   - Always link concrete source evidence (e.g., file paths/URIs, documentation, or commit references) to the claim.
   - Assign appropriate tags and an accurate confidence score (0.0 to 1.0).

3. **Handling Evolving Knowledge & Inconsistencies:**
   - When a past claim becomes obsolete or superseded by a new decision, use `remember` with `contradicts: ["<old_claim_id>"]` to maintain an explicit lineage and prevent hallucinating outdated guidance.
   - If a claim was erroneous or should be discarded, soft-delete it with `forget` providing a concise audit reason.
