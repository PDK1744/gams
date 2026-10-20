package engine

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/PDK1744/gams/internal/audit"
	"github.com/PDK1744/gams/internal/auth"
	"github.com/PDK1744/gams/internal/config"
	"github.com/PDK1744/gams/internal/db"
	"github.com/PDK1744/gams/internal/scoring"
	"github.com/PDK1744/gams/pkg/types"
)

var (
	ErrNotFound       = errors.New("not found")
	ErrConflict       = errors.New("conflict")
	ErrInvalidInput   = errors.New("invalid input")
	ErrForbidden      = errors.New("forbidden")
	ErrUnauthorized   = errors.New("unauthorized")
	ErrMaxClaimsLimit = errors.New("max claims limit reached")

	slugRegex = regexp.MustCompile(`^[a-z0-9_-]+$`)
	ftsWordRe = regexp.MustCompile(`[\p{L}\p{N}]+`)
)

type Engine struct {
	db          *db.DB
	authSvc     *auth.Service
	auditLogger *audit.Logger
	cfg         *config.Config
	version     string
}

func New(database *db.DB, authSvc *auth.Service, cfg *config.Config, version string) *Engine {
	return &Engine{
		db:          database,
		authSvc:     authSvc,
		auditLogger: audit.NewLogger(version),
		cfg:         cfg,
		version:     version,
	}
}

// DB returns the underlying database handle.
func (e *Engine) DB() *db.DB {
	return e.db
}

// Auth returns the authentication service.
func (e *Engine) Auth() *auth.Service {
	return e.authSvc
}

// Audit returns the audit logger.
func (e *Engine) Audit() *audit.Logger {
	return e.auditLogger
}

// --- Remember Write Path ---

func (e *Engine) Remember(ctx context.Context, actor *types.APIKey, tenant *types.Tenant, req *types.RememberRequest) (*types.RememberResponse, error) {
	if strings.TrimSpace(req.Content) == "" {
		return nil, fmt.Errorf("%w: content cannot be empty", ErrInvalidInput)
	}
	if len(req.Content) > e.cfg.Limits.MaxClaimSizeBytes {
		return nil, fmt.Errorf("%w: content size %d exceeds limit %d", ErrInvalidInput, len(req.Content), e.cfg.Limits.MaxClaimSizeBytes)
	}
	if req.Confidence < 0.0 || req.Confidence > 1.0 {
		return nil, fmt.Errorf("%w: confidence must be between 0.0 and 1.0", ErrInvalidInput)
	}
	if len(req.Evidence) == 0 {
		return nil, fmt.Errorf("%w: evidence must contain at least 1 item", ErrInvalidInput)
	}
	if len(req.Evidence) > e.cfg.Limits.MaxEvidencePerClaim {
		return nil, fmt.Errorf("%w: evidence items %d exceeds limit %d", ErrInvalidInput, len(req.Evidence), e.cfg.Limits.MaxEvidencePerClaim)
	}
	for _, ev := range req.Evidence {
		if strings.TrimSpace(ev.URI) == "" || strings.TrimSpace(ev.Description) == "" {
			return nil, fmt.Errorf("%w: evidence uri and description are required", ErrInvalidInput)
		}
		switch ev.EvidenceType {
		case "url", "file", "tool_output", "agent_assertion", "human":
		default:
			return nil, fmt.Errorf("%w: invalid evidence_type %q", ErrInvalidInput, ev.EvidenceType)
		}
	}

	// Resolve namespace
	nsSlug := strings.TrimSpace(req.Namespace)
	if nsSlug == "" {
		nsSlug = "default"
	}

	ns, err := e.resolveOrCreateNamespace(ctx, tenant, nsSlug, actor)
	if err != nil {
		return nil, err
	}

	// Verify namespace access for write
	if err := e.authSvc.CheckNamespaceAccess(ctx, actor, ns, "write"); err != nil {
		if errors.Is(err, auth.ErrNamespaceArchived) {
			return nil, fmt.Errorf("%w: %v", ErrForbidden, err)
		}
		return nil, fmt.Errorf("%w: %v", ErrForbidden, err)
	}

	// Check max claims caps if configured
	if tenant.MaxClaims > 0 {
		var count int64
		_ = e.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM claims WHERE tenant_id = ? AND status != 'forgotten'", tenant.ID).Scan(&count)
		if count >= tenant.MaxClaims {
			return nil, fmt.Errorf("%w: tenant cap of %d active claims reached", ErrMaxClaimsLimit, tenant.MaxClaims)
		}
	}
	if ns.MaxClaims > 0 {
		var count int64
		_ = e.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM claims WHERE namespace_id = ? AND status != 'forgotten'", ns.ID).Scan(&count)
		if count >= ns.MaxClaims {
			return nil, fmt.Errorf("%w: namespace cap of %d active claims reached", ErrMaxClaimsLimit, ns.MaxClaims)
		}
	}

	// Calculate canonical content hash: sha256(canonical(content + tenant_id + namespace_id))
	contentHash := calculateContentHash(req.Content, tenant.ID, ns.ID)
	now := types.NowNano()

	// Validate that actor has permission to contradict referenced claims before starting transaction
	for _, contradictedID := range req.Contradicts {
		var cNamespaceID string
		err := e.db.QueryRowContext(ctx,
			`SELECT namespace_id FROM claims WHERE id = ? AND tenant_id = ? AND status != 'forgotten'`,
			contradictedID, tenant.ID).Scan(&cNamespaceID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return nil, fmt.Errorf("checking contradicted claim: %w", err)
		}

		cNS, err := e.GetNamespace(ctx, tenant.ID, cNamespaceID)
		if err != nil {
			return nil, fmt.Errorf("resolving namespace for contradicted claim: %w", err)
		}
		if err := e.authSvc.CheckNamespaceAccess(ctx, actor, cNS, "write"); err != nil {
			return nil, fmt.Errorf("%w: unauthorized to contradict claim %q in namespace %q", ErrForbidden, contradictedID, cNS.Slug)
		}
	}

	// Begin IMMEDIATE transaction for serialization
	tx, err := e.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback()

	// Case 1: Explicit claim_id supplied for update
	if req.ClaimID != nil && *req.ClaimID != "" {
		var claim types.Claim
		row := tx.QueryRowContext(ctx,
			`SELECT id, tenant_id, namespace_id, current_version, content, content_hash, confidence, status, created_at, updated_at
			 FROM claims WHERE id = ? AND tenant_id = ?`,
			*req.ClaimID, tenant.ID)
		if err := row.Scan(&claim.ID, &claim.TenantID, &claim.NamespaceID, &claim.CurrentVersion, &claim.Content,
			&claim.ContentHash, &claim.Confidence, &claim.Status, &claim.CreatedAt, &claim.UpdatedAt); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("%w: claim %q not found", ErrNotFound, *req.ClaimID)
			}
			return nil, fmt.Errorf("querying claim: %w", err)
		}

		if claim.Status == "forgotten" {
			return nil, fmt.Errorf("%w: cannot update forgotten claim", ErrForbidden)
		}

		// Optimistic concurrency check
		if req.BaseVersion != nil && claim.CurrentVersion != *req.BaseVersion {
			return &types.RememberResponse{
				ClaimID:   claim.ID,
				Version:   claim.CurrentVersion,
				CreatedAt: types.FormatISO8601(claim.CreatedAt),
				Status:    "conflict",
				Conflict: &types.ConflictInfo{
					CurrentVersion: claim.CurrentVersion,
					UpdatedAt:      types.FormatISO8601(claim.UpdatedAt),
				},
			}, ErrConflict
		}

		newVersion := claim.CurrentVersion + 1
		tagsJSON := marshalJSONOr(req.Tags, "[]")
		metaJSON := marshalJSONOr(req.Metadata, "{}")

		_, err = tx.ExecContext(ctx,
			`UPDATE claims SET current_version = ?, content = ?, content_hash = ?, confidence = ?, status = 'active',
			                   writer_id = ?, writer_label = ?, tags = ?, updated_at = ?, metadata = ?
			 WHERE id = ?`,
			newVersion, req.Content, contentHash, req.Confidence, actor.ID, actor.Label, tagsJSON, now, metaJSON, claim.ID)
		if err != nil {
			return nil, fmt.Errorf("updating claim: %w", err)
		}

		// Insert claim version
		contradictsJSON := marshalJSONOr(req.Contradicts, "[]")
		verID := types.NewUUIDv7()
		_, err = tx.ExecContext(ctx,
			`INSERT INTO claim_versions (id, claim_id, tenant_id, namespace_id, version, content, confidence, contradicts, writer_id, written_at, change_reason)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			verID, claim.ID, tenant.ID, ns.ID, newVersion, req.Content, req.Confidence, contradictsJSON, actor.ID, now, req.ChangeReason)
		if err != nil {
			return nil, fmt.Errorf("inserting claim version: %w", err)
		}

		// Insert evidence
		if err := e.insertEvidence(ctx, tx, claim.ID, newVersion, tenant.ID, req.Evidence, now); err != nil {
			return nil, err
		}

		// Update recall index
		_, _ = tx.ExecContext(ctx, "DELETE FROM recall_index WHERE claim_id = ?", claim.ID)
		_, err = tx.ExecContext(ctx,
			`INSERT INTO recall_index (claim_id, tenant_id, namespace_id, content, tags, namespace_slug)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			claim.ID, tenant.ID, ns.ID, req.Content, strings.Join(req.Tags, " "), ns.Slug)
		if err != nil {
			return nil, fmt.Errorf("updating recall index: %w", err)
		}

		// Audit log WRITE
		if err := e.auditLogger.Log(ctx, tx, &types.AuditEvent{
			TenantID:     tenant.ID,
			NamespaceID:  &ns.ID,
			EventType:    audit.EventWrite,
			ActorKeyID:   actor.ID,
			ActorLabel:   actor.Label,
			ClaimID:      &claim.ID,
			ClaimVersion: &newVersion,
			Payload: map[string]any{
				"action":  "update",
				"version": newVersion,
			},
			OccurredAt: now,
		}); err != nil {
			return nil, err
		}

		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("committing transaction: %w", err)
		}

		return &types.RememberResponse{
			ClaimID:   claim.ID,
			Version:   newVersion,
			CreatedAt: types.FormatISO8601(claim.CreatedAt),
			Status:    "updated",
		}, nil
	}

	// Case 2: Idempotency check by content hash per tenant + namespace
	var existingClaim types.Claim
	row := tx.QueryRowContext(ctx,
		`SELECT id, tenant_id, namespace_id, current_version, content, content_hash, confidence, status, created_at, updated_at
		 FROM claims
		 WHERE tenant_id = ? AND namespace_id = ? AND content_hash = ? AND status = 'active'`,
		tenant.ID, ns.ID, contentHash)
	err = row.Scan(&existingClaim.ID, &existingClaim.TenantID, &existingClaim.NamespaceID, &existingClaim.CurrentVersion,
		&existingClaim.Content, &existingClaim.ContentHash, &existingClaim.Confidence, &existingClaim.Status,
		&existingClaim.CreatedAt, &existingClaim.UpdatedAt)

	if err == nil {
		// Existing active claim with matching canonical content
		if req.BaseVersion != nil && existingClaim.CurrentVersion != *req.BaseVersion {
			return &types.RememberResponse{
				ClaimID:   existingClaim.ID,
				Version:   existingClaim.CurrentVersion,
				CreatedAt: types.FormatISO8601(existingClaim.CreatedAt),
				Status:    "conflict",
				Conflict: &types.ConflictInfo{
					CurrentVersion: existingClaim.CurrentVersion,
					UpdatedAt:      types.FormatISO8601(existingClaim.UpdatedAt),
				},
			}, ErrConflict
		}

		// Idempotent hit: return existing claim
		return &types.RememberResponse{
			ClaimID:   existingClaim.ID,
			Version:   existingClaim.CurrentVersion,
			CreatedAt: types.FormatISO8601(existingClaim.CreatedAt),
			Status:    "created",
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("checking idempotency: %w", err)
	}

	// Case 3: Create brand new claim
	claimID := types.NewUUIDv7()
	tagsJSON := marshalJSONOr(req.Tags, "[]")
	metaJSON := marshalJSONOr(req.Metadata, "{}")
	contradictsJSON := marshalJSONOr(req.Contradicts, "[]")

	_, err = tx.ExecContext(ctx,
		`INSERT INTO claims (id, tenant_id, namespace_id, current_version, content, content_hash, confidence, status, writer_id, writer_label, tags, created_at, updated_at, metadata)
		 VALUES (?, ?, ?, 1, ?, ?, ?, 'active', ?, ?, ?, ?, ?, ?)`,
		claimID, tenant.ID, ns.ID, req.Content, contentHash, req.Confidence, actor.ID, actor.Label, tagsJSON, now, now, metaJSON)
	if err != nil {
		return nil, fmt.Errorf("inserting claim: %w", err)
	}

	verID := types.NewUUIDv7()
	_, err = tx.ExecContext(ctx,
		`INSERT INTO claim_versions (id, claim_id, tenant_id, namespace_id, version, content, confidence, contradicts, writer_id, written_at, change_reason)
		 VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?)`,
		verID, claimID, tenant.ID, ns.ID, req.Content, req.Confidence, contradictsJSON, actor.ID, now, req.ChangeReason)
	if err != nil {
		return nil, fmt.Errorf("inserting claim version: %w", err)
	}

	if err := e.insertEvidence(ctx, tx, claimID, 1, tenant.ID, req.Evidence, now); err != nil {
		return nil, err
	}

	// Insert into FTS5 index
	_, err = tx.ExecContext(ctx,
		`INSERT INTO recall_index (claim_id, tenant_id, namespace_id, content, tags, namespace_slug)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		claimID, tenant.ID, ns.ID, req.Content, strings.Join(req.Tags, " "), ns.Slug)
	if err != nil {
		return nil, fmt.Errorf("indexing in recall_index: %w", err)
	}

	// Handle explicit contradictions
	for _, contradictedID := range req.Contradicts {
		res, err := tx.ExecContext(ctx,
			`UPDATE claims SET status = 'contradicted', contradicted_by = ?, updated_at = ?
			 WHERE id = ? AND tenant_id = ? AND status != 'forgotten'`,
			claimID, now, contradictedID, tenant.ID)
		if err == nil {
			if rowsAffected, _ := res.RowsAffected(); rowsAffected > 0 {
				_ = e.auditLogger.Log(ctx, tx, &types.AuditEvent{
					TenantID:    tenant.ID,
					NamespaceID: &ns.ID,
					EventType:   audit.EventContradict,
					ActorKeyID:  actor.ID,
					ActorLabel:  actor.Label,
					ClaimID:     &contradictedID,
					Payload: map[string]any{
						"contradicted_by": claimID,
					},
					OccurredAt: now,
				})
			}
		}
	}

	// Audit log WRITE
	var v1 int64 = 1
	if err := e.auditLogger.Log(ctx, tx, &types.AuditEvent{
		TenantID:     tenant.ID,
		NamespaceID:  &ns.ID,
		EventType:    audit.EventWrite,
		ActorKeyID:   actor.ID,
		ActorLabel:   actor.Label,
		ClaimID:      &claimID,
		ClaimVersion: &v1,
		Payload: map[string]any{
			"action":  "create",
			"version": 1,
		},
		OccurredAt: now,
	}); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing transaction: %w", err)
	}

	return &types.RememberResponse{
		ClaimID:   claimID,
		Version:   1,
		CreatedAt: types.FormatISO8601(now),
		Status:    "created",
	}, nil
}

func (e *Engine) insertEvidence(ctx context.Context, tx *sql.Tx, claimID string, version int64, tenantID string, items []types.EvidenceInput, defaultTime int64) error {
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO evidence (id, claim_id, claim_version, tenant_id, uri, description, evidence_type, captured_at, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, '{}')`)
	if err != nil {
		return fmt.Errorf("preparing evidence insert: %w", err)
	}
	defer stmt.Close()

	for _, item := range items {
		evID := types.NewUUIDv7()
		capturedAt := defaultTime
		if item.CapturedAt != "" {
			if parsed, err := types.ParseISO8601(item.CapturedAt); err == nil && parsed > 0 {
				capturedAt = parsed
			}
		}
		if _, err := stmt.ExecContext(ctx, evID, claimID, version, tenantID, item.URI, item.Description, item.EvidenceType, capturedAt); err != nil {
			return fmt.Errorf("inserting evidence item: %w", err)
		}
	}
	return nil
}

// --- Recall Read Path ---

func (e *Engine) Recall(ctx context.Context, actor *types.APIKey, tenant *types.Tenant, req *types.RecallRequest) (*types.RecallResponse, error) {
	start := time.Now()

	limit := req.Limit
	if limit <= 0 {
		limit = e.cfg.Limits.DefaultRecallLimit
	}
	if limit > 100 {
		limit = 100
	}

	queryStr := strings.TrimSpace(req.Query)
	if queryStr == "" {
		return &types.RecallResponse{Claims: []types.RecalledClaim{}, TotalMatched: 0, QueryMS: time.Since(start).Milliseconds()}, nil
	}

	// Namespace filter resolution
	var allowedNamespaceIDs []string
	if req.Namespace != "" {
		ns, err := e.GetNamespace(ctx, tenant.ID, req.Namespace)
		if err != nil {
			return nil, err
		}
		if err := e.authSvc.CheckNamespaceAccess(ctx, actor, ns, "read"); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrForbidden, err)
		}
		allowedNamespaceIDs = []string{ns.ID}
	} else {
		// All accessible namespaces for this key
		namespaces, err := e.ListNamespaces(ctx, tenant.ID, actor)
		if err != nil {
			return nil, err
		}
		for _, ns := range namespaces {
			allowedNamespaceIDs = append(allowedNamespaceIDs, ns.ID)
		}
		if len(allowedNamespaceIDs) == 0 {
			return &types.RecallResponse{Claims: []types.RecalledClaim{}, TotalMatched: 0, QueryMS: time.Since(start).Milliseconds()}, nil
		}
	}

	// Build FTS5 query terms
	words := ftsWordRe.FindAllString(queryStr, -1)
	if len(words) == 0 {
		return &types.RecallResponse{Claims: []types.RecalledClaim{}, TotalMatched: 0, QueryMS: time.Since(start).Milliseconds()}, nil
	}

	var ftsClauses []string
	for _, w := range words {
		ftsClauses = append(ftsClauses, `"`+w+`"`)
	}
	ftsMatch := strings.Join(ftsClauses, " OR ")

	// Build query conditions
	var conditions []string
	var args []any

	conditions = append(conditions, "recall_index MATCH ?")
	args = append(args, ftsMatch)

	conditions = append(conditions, "c.tenant_id = ?")
	args = append(args, tenant.ID)

	placeholders := make([]string, len(allowedNamespaceIDs))
	for i, id := range allowedNamespaceIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	conditions = append(conditions, fmt.Sprintf("c.namespace_id IN (%s)", strings.Join(placeholders, ",")))

	if req.ConfidenceMin > 0 {
		conditions = append(conditions, "c.confidence >= ?")
		args = append(args, req.ConfidenceMin)
	}

	// Status filtering
	if !req.IncludeContradicted && !req.IncludeForgotten {
		conditions = append(conditions, "c.status = 'active'")
	} else if req.IncludeContradicted && !req.IncludeForgotten {
		conditions = append(conditions, "c.status IN ('active', 'contradicted')")
	} else if !req.IncludeContradicted && req.IncludeForgotten {
		conditions = append(conditions, "c.status IN ('active', 'forgotten')")
	} // if both, no status condition needed

	// Recency window filter
	now := types.NowNano()
	if req.RecencyWindowDays > 0 {
		cutoff := now - int64(req.RecencyWindowDays)*24*int64(time.Hour)
		conditions = append(conditions, "c.updated_at >= ?")
		args = append(args, cutoff)
	}

	// Tags filter (AND condition)
	for _, tag := range req.Tags {
		conditions = append(conditions, "EXISTS (SELECT 1 FROM json_each(c.tags) WHERE value = ?)")
		args = append(args, tag)
	}

	sqlQuery := fmt.Sprintf(`SELECT c.id, c.content, c.confidence, c.current_version, c.status, c.tags,
	               c.created_at, c.updated_at, c.writer_label,
	               ns.id, ns.slug, ns.recall_decay_lambda,
	               recall_index.rank
	        FROM recall_index
	        JOIN claims c ON c.id = recall_index.claim_id
	        JOIN namespaces ns ON ns.id = c.namespace_id
	        WHERE %s`, strings.Join(conditions, " AND "))

	rows, err := e.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("executing recall query: %w", err)
	}
	defer rows.Close()

	type rawMatch struct {
		claim       types.RecalledClaim
		rank        float64
		decay       float64
		updatedNano int64
	}

	var matches []rawMatch
	var rawRanks []float64

	for rows.Next() {
		var m rawMatch
		var tagsJSON string
		var nsDecay sql.NullFloat64
		var createdNano int64

		err := rows.Scan(
			&m.claim.ClaimID, &m.claim.Content, &m.claim.Confidence, &m.claim.Version, &m.claim.Status,
			&tagsJSON, &createdNano, &m.updatedNano, &m.claim.WriterLabel,
			&m.claim.NamespaceID, &m.claim.NamespaceSlug, &nsDecay,
			&m.rank,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning recall row: %w", err)
		}

		m.claim.CreatedAt = types.FormatISO8601(createdNano)
		m.claim.UpdatedAt = types.FormatISO8601(m.updatedNano)
		if tagsJSON != "" && tagsJSON != "[]" {
			_ = json.Unmarshal([]byte(tagsJSON), &m.claim.Tags)
		}

		lambda := tenant.RecallDecayLambda
		if nsDecay.Valid && nsDecay.Float64 > 0 {
			lambda = nsDecay.Float64
		}
		m.decay = lambda

		rawRanks = append(rawRanks, m.rank)
		matches = append(matches, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating recall rows: %w", err)
	}

	totalMatched := len(matches)
	if totalMatched == 0 {
		return &types.RecallResponse{
			Claims:       []types.RecalledClaim{},
			TotalMatched: 0,
			QueryMS:      time.Since(start).Milliseconds(),
		}, nil
	}

	// Normalize BM25 ranks
	normBM25 := scoring.NormalizeBM25(rawRanks)

	// Compute composite score using updatedNano directly without string conversion
	for i := range matches {
		m := &matches[i]
		bm25Score := normBM25[i]
		recency := scoring.RecencyScore(m.updatedNano, now, m.decay)
		m.claim.Score = scoring.Composite(bm25Score, m.claim.Confidence, recency)
	}

	// Sort descending by composite score
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].claim.Score > matches[j].claim.Score
	})

	// Truncate to limit BEFORE fetching evidence (avoids N+1 query and discard bottleneck)
	if len(matches) > limit {
		matches = matches[:limit]
	}

	results := make([]types.RecalledClaim, len(matches))
	for i, m := range matches {
		results[i] = m.claim
	}

	// Batch fetch evidence in a single query for the top-K claims
	if len(results) > 0 {
		e.batchPopulateEvidence(ctx, results)
	}

	// Audit log READ
	_ = e.auditLogger.Log(ctx, e.db.DB, &types.AuditEvent{
		TenantID:   tenant.ID,
		EventType:  audit.EventRead,
		ActorKeyID: actor.ID,
		ActorLabel: actor.Label,
		Payload: map[string]any{
			"query":         queryStr,
			"total_matched": totalMatched,
			"returned":      len(results),
		},
		OccurredAt: now,
	})

	return &types.RecallResponse{
		Claims:       results,
		TotalMatched: totalMatched,
		QueryMS:      time.Since(start).Milliseconds(),
	}, nil
}

func (e *Engine) batchPopulateEvidence(ctx context.Context, claims []types.RecalledClaim) {
	if len(claims) == 0 {
		return
	}

	placeholders := make([]string, len(claims))
	args := make([]any, len(claims))
	for i, c := range claims {
		placeholders[i] = "?"
		args[i] = c.ClaimID
	}

	query := fmt.Sprintf(`SELECT claim_id, claim_version, uri, description, evidence_type, captured_at
	                      FROM evidence
	                      WHERE claim_id IN (%s)`, strings.Join(placeholders, ","))

	rows, err := e.db.QueryContext(ctx, query, args...)
	if err != nil {
		return
	}
	defer rows.Close()

	type evKey struct {
		claimID string
		version int64
	}
	evMap := make(map[evKey][]types.EvidenceInput)

	for rows.Next() {
		var cID string
		var cVer int64
		var ev types.EvidenceInput
		var capturedNano int64
		if err := rows.Scan(&cID, &cVer, &ev.URI, &ev.Description, &ev.EvidenceType, &capturedNano); err == nil {
			ev.CapturedAt = types.FormatISO8601(capturedNano)
			k := evKey{claimID: cID, version: cVer}
			evMap[k] = append(evMap[k], ev)
		}
	}

	for i := range claims {
		k := evKey{claimID: claims[i].ClaimID, version: claims[i].Version}
		if evList, ok := evMap[k]; ok {
			claims[i].Evidence = evList
		} else {
			claims[i].Evidence = []types.EvidenceInput{}
		}
	}
}

// --- Forget Soft Delete ---

func (e *Engine) Forget(ctx context.Context, actor *types.APIKey, tenant *types.Tenant, claimID string, reason string) (*types.ForgetResponse, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, fmt.Errorf("%w: forget reason is required", ErrInvalidInput)
	}

	var claim types.Claim
	var forgottenAt sql.NullInt64
	err := e.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, namespace_id, current_version, writer_id, status, forgotten_at
		 FROM claims WHERE id = ? AND tenant_id = ?`,
		claimID, tenant.ID).Scan(
		&claim.ID, &claim.TenantID, &claim.NamespaceID, &claim.CurrentVersion, &claim.WriterID, &claim.Status, &forgottenAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: claim %q not found", ErrNotFound, claimID)
		}
		return nil, fmt.Errorf("querying claim: %w", err)
	}

	// Authorization check: only original writer, namespace admin, or server admin
	if actor != nil && actor.ID != claim.WriterID {
		ns, err := e.GetNamespace(ctx, tenant.ID, claim.NamespaceID)
		if err != nil || e.authSvc.CheckNamespaceAccess(ctx, actor, ns, "admin") != nil {
			return nil, fmt.Errorf("%w: only the writer, namespace admin, or server admin may forget this claim", ErrForbidden)
		}
	}

	if claim.Status == "forgotten" && forgottenAt.Valid {
		return &types.ForgetResponse{
			ClaimID:     claimID,
			ForgottenAt: types.FormatISO8601(forgottenAt.Int64),
		}, nil
	}

	now := types.NowNano()
	tx, err := e.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		`UPDATE claims SET status = 'forgotten', forgotten_at = ?, forgotten_reason = ?, updated_at = ?
		 WHERE id = ?`,
		now, reason, now, claimID)
	if err != nil {
		return nil, fmt.Errorf("updating claim status: %w", err)
	}

	// Remove from FTS5 index
	_, err = tx.ExecContext(ctx, "DELETE FROM recall_index WHERE claim_id = ?", claimID)
	if err != nil {
		return nil, fmt.Errorf("removing from recall index: %w", err)
	}

	actorKeyID := "admin"
	actorLabel := "admin"
	if actor != nil {
		actorKeyID = actor.ID
		actorLabel = actor.Label
	}

	// Audit log FORGET
	if err := e.auditLogger.Log(ctx, tx, &types.AuditEvent{
		TenantID:     tenant.ID,
		NamespaceID:  &claim.NamespaceID,
		EventType:    audit.EventForget,
		ActorKeyID:   actorKeyID,
		ActorLabel:   actorLabel,
		ClaimID:      &claimID,
		ClaimVersion: &claim.CurrentVersion,
		Payload: map[string]any{
			"reason": reason,
		},
		OccurredAt: now,
	}); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing transaction: %w", err)
	}

	return &types.ForgetResponse{
		ClaimID:     claimID,
		ForgottenAt: types.FormatISO8601(now),
	}, nil
}

// --- Inspect Claim History ---

func (e *Engine) Inspect(ctx context.Context, actor *types.APIKey, tenant *types.Tenant, claimID string) (*types.InspectResponse, error) {
	var claim types.Claim
	var metaJSON, tagsJSON string
	var contradictedBy, forgottenReason sql.NullString
	var forgottenAt sql.NullInt64

	row := e.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, namespace_id, current_version, content, content_hash, confidence, status,
		        contradicted_by, writer_id, writer_label, tags, created_at, updated_at, forgotten_at, forgotten_reason, metadata
		 FROM claims WHERE id = ? AND tenant_id = ?`,
		claimID, tenant.ID)
	err := row.Scan(
		&claim.ID, &claim.TenantID, &claim.NamespaceID, &claim.CurrentVersion, &claim.Content, &claim.ContentHash,
		&claim.Confidence, &claim.Status, &contradictedBy, &claim.WriterID, &claim.WriterLabel,
		&tagsJSON, &claim.CreatedAt, &claim.UpdatedAt, &forgottenAt, &forgottenReason, &metaJSON,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: claim %q not found", ErrNotFound, claimID)
		}
		return nil, fmt.Errorf("querying claim: %w", err)
	}

	if contradictedBy.Valid {
		claim.ContradictedBy = &contradictedBy.String
	}
	if forgottenAt.Valid {
		claim.ForgottenAt = &forgottenAt.Int64
	}
	if forgottenReason.Valid {
		claim.ForgottenReason = &forgottenReason.String
	}
	if tagsJSON != "" && tagsJSON != "[]" {
		_ = json.Unmarshal([]byte(tagsJSON), &claim.Tags)
	}
	if metaJSON != "" && metaJSON != "{}" {
		_ = json.Unmarshal([]byte(metaJSON), &claim.Metadata)
	}

	// Verify namespace access
	if actor != nil {
		ns, err := e.GetNamespace(ctx, tenant.ID, claim.NamespaceID)
		if err != nil {
			return nil, err
		}
		if err := e.authSvc.CheckNamespaceAccess(ctx, actor, ns, "read"); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrForbidden, err)
		}
	}

	// Fetch versions
	verRows, err := e.db.QueryContext(ctx,
		`SELECT id, claim_id, tenant_id, namespace_id, version, content, confidence, contradicts, writer_id, written_at, change_reason
		 FROM claim_versions WHERE claim_id = ? ORDER BY version ASC`, claimID)
	if err != nil {
		return nil, fmt.Errorf("querying versions: %w", err)
	}
	defer verRows.Close()

	var versions []types.ClaimVersion
	for verRows.Next() {
		var v types.ClaimVersion
		var contraJSON string
		var chgReason sql.NullString
		if err := verRows.Scan(&v.ID, &v.ClaimID, &v.TenantID, &v.NamespaceID, &v.Version, &v.Content, &v.Confidence, &contraJSON, &v.WriterID, &v.WrittenAt, &chgReason); err == nil {
			if contraJSON != "" && contraJSON != "[]" {
				_ = json.Unmarshal([]byte(contraJSON), &v.Contradicts)
			}
			if chgReason.Valid {
				v.ChangeReason = &chgReason.String
			}
			versions = append(versions, v)
		}
	}

	// Fetch evidence
	evRows, err := e.db.QueryContext(ctx,
		`SELECT id, claim_id, claim_version, tenant_id, uri, description, evidence_type, captured_at, metadata
		 FROM evidence WHERE claim_id = ? ORDER BY claim_version ASC, captured_at ASC`, claimID)
	if err != nil {
		return nil, fmt.Errorf("querying evidence: %w", err)
	}
	defer evRows.Close()

	var evidence []types.Evidence
	for evRows.Next() {
		var ev types.Evidence
		var evMeta string
		if err := evRows.Scan(&ev.ID, &ev.ClaimID, &ev.ClaimVersion, &ev.TenantID, &ev.URI, &ev.Description, &ev.EvidenceType, &ev.CapturedAt, &evMeta); err == nil {
			if evMeta != "" && evMeta != "{}" {
				_ = json.Unmarshal([]byte(evMeta), &ev.Metadata)
			}
			evidence = append(evidence, ev)
		}
	}

	// Fetch audit trail for this specific claim using SQL filter
	claimAudit, err := e.auditLogger.Query(ctx, e.db.DB, audit.Filter{
		TenantID: tenant.ID,
		ClaimID:  &claimID,
		Limit:    100,
	})
	if err != nil {
		claimAudit = []types.AuditEvent{}
	}

	return &types.InspectResponse{
		Claim:    &claim,
		Versions: versions,
		Evidence: evidence,
		Audit:    claimAudit,
	}, nil
}

// --- Namespace Operations ---

func (e *Engine) resolveOrCreateNamespace(ctx context.Context, tenant *types.Tenant, slug string, actor *types.APIKey) (*types.Namespace, error) {
	ns, err := e.GetNamespace(ctx, tenant.ID, slug)
	if err == nil {
		return ns, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	// Auto-create namespace if enabled on tenant
	if !tenant.AutoCreateNamespaces {
		return nil, fmt.Errorf("%w: %q", auth.ErrNamespaceNotFound, slug)
	}

	if !slugRegex.MatchString(slug) {
		return nil, fmt.Errorf("%w: slug %q must contain only lowercase letters, numbers, hyphens, and underscores", ErrInvalidInput, slug)
	}

	now := types.NowNano()
	newNS := &types.Namespace{
		ID:         types.NewUUIDv7(),
		TenantID:   tenant.ID,
		Slug:       slug,
		Name:       slug,
		Status:     "active",
		AccessMode: "tenant_wide",
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	_, err = e.db.ExecContext(ctx,
		`INSERT INTO namespaces (id, tenant_id, slug, name, status, access_mode, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'active', 'tenant_wide', ?, ?)`,
		newNS.ID, tenant.ID, newNS.Slug, newNS.Name, now, now)
	if err != nil {
		return nil, fmt.Errorf("auto-creating namespace: %w", err)
	}

	actorKeyID := "system"
	actorLabel := "system"
	if actor != nil {
		actorKeyID = actor.ID
		actorLabel = actor.Label
	}

	_ = e.auditLogger.Log(ctx, e.db.DB, &types.AuditEvent{
		TenantID:    tenant.ID,
		NamespaceID: &newNS.ID,
		EventType:   audit.EventNamespaceCreated,
		ActorKeyID:  actorKeyID,
		ActorLabel:  actorLabel,
		Payload: map[string]any{
			"auto_created": true,
			"slug":         slug,
		},
		OccurredAt: now,
	})

	return newNS, nil
}

func (e *Engine) CreateNamespace(ctx context.Context, tenantID string, req *types.CreateNamespaceRequest, actorID, actorLabel string) (*types.Namespace, error) {
	slug := strings.TrimSpace(req.Slug)
	name := strings.TrimSpace(req.Name)
	if slug == "" {
		return nil, fmt.Errorf("%w: namespace slug is required", ErrInvalidInput)
	}
	if !slugRegex.MatchString(slug) {
		return nil, fmt.Errorf("%w: slug must contain only lowercase letters, numbers, hyphens, and underscores", ErrInvalidInput)
	}
	if name == "" {
		name = slug
	}

	accessMode := req.AccessMode
	if accessMode == "" {
		accessMode = "tenant_wide"
	}
	if accessMode != "tenant_wide" && accessMode != "key_restricted" {
		return nil, fmt.Errorf("%w: access_mode must be 'tenant_wide' or 'key_restricted'", ErrInvalidInput)
	}

	now := types.NowNano()
	nsID := types.NewUUIDv7()
	metaJSON := marshalJSONOr(req.Metadata, "{}")

	_, err := e.db.ExecContext(ctx,
		`INSERT INTO namespaces (id, tenant_id, slug, name, description, status, access_mode, recall_decay_lambda, max_claims, created_at, updated_at, metadata)
		 VALUES (?, ?, ?, ?, ?, 'active', ?, ?, ?, ?, ?, ?)`,
		nsID, tenantID, slug, name, req.Description, accessMode, req.RecallDecayLambda, req.MaxClaims, now, now, metaJSON)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("%w: namespace slug %q already exists in tenant", ErrConflict, slug)
		}
		return nil, fmt.Errorf("creating namespace: %w", err)
	}

	_ = e.auditLogger.Log(ctx, e.db.DB, &types.AuditEvent{
		TenantID:    tenantID,
		NamespaceID: &nsID,
		EventType:   audit.EventNamespaceCreated,
		ActorKeyID:  actorID,
		ActorLabel:  actorLabel,
		Payload: map[string]any{
			"slug":        slug,
			"access_mode": accessMode,
		},
		OccurredAt: now,
	})

	return e.GetNamespace(ctx, tenantID, nsID)
}

func (e *Engine) GetNamespace(ctx context.Context, tenantID, slugOrID string) (*types.Namespace, error) {
	var ns types.Namespace
	var desc sql.NullString
	var decay sql.NullFloat64
	var metaJSON string

	row := e.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, slug, name, description, status, access_mode, recall_decay_lambda, max_claims, created_at, updated_at, metadata
		 FROM namespaces WHERE tenant_id = ? AND (slug = ? OR id = ?)`,
		tenantID, slugOrID, slugOrID)
	err := row.Scan(&ns.ID, &ns.TenantID, &ns.Slug, &ns.Name, &desc, &ns.Status, &ns.AccessMode,
		&decay, &ns.MaxClaims, &ns.CreatedAt, &ns.UpdatedAt, &metaJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: namespace %q not found", ErrNotFound, slugOrID)
		}
		return nil, fmt.Errorf("querying namespace: %w", err)
	}

	if desc.Valid {
		ns.Description = &desc.String
	}
	if decay.Valid {
		ns.RecallDecayLambda = &decay.Float64
	}
	if metaJSON != "" && metaJSON != "{}" {
		_ = json.Unmarshal([]byte(metaJSON), &ns.Metadata)
	}
	return &ns, nil
}

func (e *Engine) GetNamespaceDetail(ctx context.Context, tenantID, slugOrID string) (*types.NamespaceDetailResponse, error) {
	ns, err := e.GetNamespace(ctx, tenantID, slugOrID)
	if err != nil {
		return nil, err
	}

	var totalCount int64
	_ = e.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM claims WHERE namespace_id = ?", ns.ID).Scan(&totalCount)

	statusMap := map[string]int64{"active": 0, "contradicted": 0, "forgotten": 0}
	rows, err := e.db.QueryContext(ctx, "SELECT status, COUNT(*) FROM claims WHERE namespace_id = ? GROUP BY status", ns.ID)
	if err == nil {
		for rows.Next() {
			var st string
			var cnt int64
			if err := rows.Scan(&st, &cnt); err == nil {
				statusMap[st] = cnt
			}
		}
		rows.Close()
	}

	var mostRecentNano sql.NullInt64
	_ = e.db.QueryRowContext(ctx, "SELECT MAX(created_at) FROM claims WHERE namespace_id = ?", ns.ID).Scan(&mostRecentNano)

	var mostRecentStr *string
	if mostRecentNano.Valid && mostRecentNano.Int64 > 0 {
		formatted := types.FormatISO8601(mostRecentNano.Int64)
		mostRecentStr = &formatted
	}

	return &types.NamespaceDetailResponse{
		Namespace: *ns,
		Stats: types.NamespaceStats{
			ClaimCount:        totalCount,
			ClaimsByStatus:    statusMap,
			MostRecentClaimAt: mostRecentStr,
		},
	}, nil
}

func (e *Engine) ListNamespaces(ctx context.Context, tenantID string, actor *types.APIKey) ([]types.Namespace, error) {
	var rows *sql.Rows
	var err error

	if actor == nil {
		// Admin call: list all namespaces
		rows, err = e.db.QueryContext(ctx,
			`SELECT id, tenant_id, slug, name, description, status, access_mode, recall_decay_lambda, max_claims, created_at, updated_at, metadata
			 FROM namespaces WHERE tenant_id = ? ORDER BY created_at ASC`, tenantID)
	} else {
		// Agent call: list accessible namespaces (tenant_wide + granted)
		rows, err = e.db.QueryContext(ctx,
			`SELECT id, tenant_id, slug, name, description, status, access_mode, recall_decay_lambda, max_claims, created_at, updated_at, metadata
			 FROM namespaces
			 WHERE tenant_id = ? AND (
			     access_mode = 'tenant_wide'
			     OR id IN (SELECT namespace_id FROM namespace_grants WHERE api_key_id = ?)
			 )
			 ORDER BY created_at ASC`, tenantID, actor.ID)
	}

	if err != nil {
		return nil, fmt.Errorf("listing namespaces: %w", err)
	}
	defer rows.Close()

	var namespaces []types.Namespace
	for rows.Next() {
		var ns types.Namespace
		var desc sql.NullString
		var decay sql.NullFloat64
		var metaJSON string

		err := rows.Scan(&ns.ID, &ns.TenantID, &ns.Slug, &ns.Name, &desc, &ns.Status, &ns.AccessMode,
			&decay, &ns.MaxClaims, &ns.CreatedAt, &ns.UpdatedAt, &metaJSON)
		if err != nil {
			return nil, fmt.Errorf("scanning namespace: %w", err)
		}
		if desc.Valid {
			ns.Description = &desc.String
		}
		if decay.Valid {
			ns.RecallDecayLambda = &decay.Float64
		}
		if metaJSON != "" && metaJSON != "{}" {
			_ = json.Unmarshal([]byte(metaJSON), &ns.Metadata)
		}
		namespaces = append(namespaces, ns)
	}
	return namespaces, rows.Err()
}

func (e *Engine) UpdateNamespace(ctx context.Context, tenantID, slugOrID string, req *types.UpdateNamespaceRequest, actorID, actorLabel string) (*types.Namespace, error) {
	ns, err := e.GetNamespace(ctx, tenantID, slugOrID)
	if err != nil {
		return nil, err
	}

	name := ns.Name
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		name = strings.TrimSpace(*req.Name)
	}
	desc := ns.Description
	if req.Description != nil {
		desc = req.Description
	}
	accessMode := ns.AccessMode
	if req.AccessMode != nil {
		if *req.AccessMode != "tenant_wide" && *req.AccessMode != "key_restricted" {
			return nil, fmt.Errorf("%w: access_mode must be 'tenant_wide' or 'key_restricted'", ErrInvalidInput)
		}
		accessMode = *req.AccessMode
	}
	status := ns.Status
	statusChanged := false
	if req.Status != nil {
		if *req.Status != "active" && *req.Status != "archived" {
			return nil, fmt.Errorf("%w: status must be 'active' or 'archived'", ErrInvalidInput)
		}
		if *req.Status != ns.Status {
			status = *req.Status
			statusChanged = true
		}
	}
	decay := ns.RecallDecayLambda
	if req.RecallDecayLambda != nil {
		decay = req.RecallDecayLambda
	}
	maxClaims := ns.MaxClaims
	if req.MaxClaims != nil {
		maxClaims = *req.MaxClaims
	}
	meta := ns.Metadata
	if req.Metadata != nil {
		meta = req.Metadata
	}
	metaJSON := marshalJSONOr(meta, "{}")
	now := types.NowNano()

	_, err = e.db.ExecContext(ctx,
		`UPDATE namespaces SET name = ?, description = ?, access_mode = ?, status = ?, recall_decay_lambda = ?, max_claims = ?, metadata = ?, updated_at = ?
		 WHERE id = ?`,
		name, desc, accessMode, status, decay, maxClaims, metaJSON, now, ns.ID)
	if err != nil {
		return nil, fmt.Errorf("updating namespace: %w", err)
	}

	if statusChanged && status == "archived" {
		_ = e.auditLogger.Log(ctx, e.db.DB, &types.AuditEvent{
			TenantID:    tenantID,
			NamespaceID: &ns.ID,
			EventType:   audit.EventNamespaceArchived,
			ActorKeyID:  actorID,
			ActorLabel:  actorLabel,
			Payload: map[string]any{
				"slug": ns.Slug,
			},
			OccurredAt: now,
		})
	}

	return e.GetNamespace(ctx, tenantID, ns.ID)
}

func (e *Engine) CreateGrant(ctx context.Context, tenantID, slugOrID string, req *types.CreateGrantRequest, actorID, actorLabel string) (*types.NamespaceGrant, error) {
	ns, err := e.GetNamespace(ctx, tenantID, slugOrID)
	if err != nil {
		return nil, err
	}

	perm := strings.ToLower(strings.TrimSpace(req.Permission))
	if perm != "read" && perm != "write" && perm != "admin" {
		return nil, fmt.Errorf("%w: permission must be 'read', 'write', or 'admin'", ErrInvalidInput)
	}

	now := types.NowNano()
	grantID := types.NewUUIDv7()

	_, err = e.db.ExecContext(ctx,
		`INSERT INTO namespace_grants (id, namespace_id, api_key_id, permission, granted_at, granted_by)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (namespace_id, api_key_id) DO UPDATE SET permission = excluded.permission, granted_at = excluded.granted_at, granted_by = excluded.granted_by`,
		grantID, ns.ID, req.APIKeyID, perm, now, actorLabel)
	if err != nil {
		return nil, fmt.Errorf("issuing grant: %w", err)
	}

	_ = e.auditLogger.Log(ctx, e.db.DB, &types.AuditEvent{
		TenantID:    tenantID,
		NamespaceID: &ns.ID,
		EventType:   audit.EventGrantIssued,
		ActorKeyID:  actorID,
		ActorLabel:  actorLabel,
		Payload: map[string]any{
			"api_key_id": req.APIKeyID,
			"permission": perm,
		},
		OccurredAt: now,
	})

	return &types.NamespaceGrant{
		ID:          grantID,
		NamespaceID: ns.ID,
		APIKeyID:    req.APIKeyID,
		Permission:  perm,
		GrantedAt:   now,
		GrantedBy:   actorLabel,
	}, nil
}

func (e *Engine) RevokeGrant(ctx context.Context, tenantID, slugOrID, apiKeyID, actorID, actorLabel string) error {
	ns, err := e.GetNamespace(ctx, tenantID, slugOrID)
	if err != nil {
		return err
	}

	res, err := e.db.ExecContext(ctx,
		"DELETE FROM namespace_grants WHERE namespace_id = ? AND api_key_id = ?",
		ns.ID, apiKeyID)
	if err != nil {
		return fmt.Errorf("revoking grant: %w", err)
	}

	if rows, _ := res.RowsAffected(); rows > 0 {
		_ = e.auditLogger.Log(ctx, e.db.DB, &types.AuditEvent{
			TenantID:    tenantID,
			NamespaceID: &ns.ID,
			EventType:   audit.EventGrantRevoked,
			ActorKeyID:  actorID,
			ActorLabel:  actorLabel,
			Payload: map[string]any{
				"api_key_id": apiKeyID,
			},
			OccurredAt: types.NowNano(),
		})
	}
	return nil
}

// --- Tenant Operations ---

func (e *Engine) CreateTenant(ctx context.Context, req *types.CreateTenantRequest, actorID, actorLabel string) (*types.Tenant, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, fmt.Errorf("%w: tenant name is required", ErrInvalidInput)
	}

	lambda := scoring.DefaultDecayLambda
	if req.RecallDecayLambda != nil && *req.RecallDecayLambda > 0 {
		lambda = *req.RecallDecayLambda
	}

	now := types.NowNano()
	tenantID := types.NewUUIDv7()
	metaJSON := marshalJSONOr(req.Metadata, "{}")

	tx, err := e.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("starting transaction: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO tenants (id, name, status, recall_decay_lambda, max_claims, auto_create_namespaces, created_at, metadata)
		 VALUES (?, ?, 'active', ?, ?, ?, ?, ?)`,
		tenantID, name, lambda, req.MaxClaims, req.AutoCreateNamespaces, now, metaJSON)
	if err != nil {
		return nil, fmt.Errorf("inserting tenant: %w", err)
	}

	// Auto-create default namespace with access_mode = tenant_wide
	defaultNSID := types.NewUUIDv7()
	_, err = tx.ExecContext(ctx,
		`INSERT INTO namespaces (id, tenant_id, slug, name, status, access_mode, created_at, updated_at)
		 VALUES (?, ?, 'default', 'Default', 'active', 'tenant_wide', ?, ?)`,
		defaultNSID, tenantID, now, now)
	if err != nil {
		return nil, fmt.Errorf("creating default namespace: %w", err)
	}

	_ = e.auditLogger.Log(ctx, tx, &types.AuditEvent{
		TenantID:   tenantID,
		EventType:  audit.EventTenantCreated,
		ActorKeyID: actorID,
		ActorLabel: actorLabel,
		Payload: map[string]any{
			"name":                   name,
			"auto_create_namespaces": req.AutoCreateNamespaces,
		},
		OccurredAt: now,
	})

	_ = e.auditLogger.Log(ctx, tx, &types.AuditEvent{
		TenantID:    tenantID,
		NamespaceID: &defaultNSID,
		EventType:   audit.EventNamespaceCreated,
		ActorKeyID:  actorID,
		ActorLabel:  actorLabel,
		Payload: map[string]any{
			"slug":        "default",
			"access_mode": "tenant_wide",
		},
		OccurredAt: now,
	})

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing transaction: %w", err)
	}

	return e.GetTenant(ctx, tenantID)
}

func (e *Engine) GetTenant(ctx context.Context, id string) (*types.Tenant, error) {
	var t types.Tenant
	var metaJSON string

	row := e.db.QueryRowContext(ctx,
		`SELECT id, name, status, recall_decay_lambda, max_claims, auto_create_namespaces, created_at, metadata
		 FROM tenants WHERE id = ?`, id)
	err := row.Scan(&t.ID, &t.Name, &t.Status, &t.RecallDecayLambda, &t.MaxClaims, &t.AutoCreateNamespaces, &t.CreatedAt, &metaJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: tenant %q not found", ErrNotFound, id)
		}
		return nil, fmt.Errorf("querying tenant: %w", err)
	}

	if metaJSON != "" && metaJSON != "{}" {
		_ = json.Unmarshal([]byte(metaJSON), &t.Metadata)
	}
	return &t, nil
}

func (e *Engine) GetTenantSummary(ctx context.Context, tenantID string) (*types.TenantSummary, error) {
	tenant, err := e.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	var totalClaims int64
	_ = e.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM claims WHERE tenant_id = ?", tenantID).Scan(&totalClaims)

	statusMap := map[string]int64{"active": 0, "contradicted": 0, "forgotten": 0}
	rows, err := e.db.QueryContext(ctx, "SELECT status, COUNT(*) FROM claims WHERE tenant_id = ? GROUP BY status", tenantID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var st string
			var count int64
			if err := rows.Scan(&st, &count); err == nil {
				statusMap[st] = count
			}
		}
	}

	now := types.NowNano()
	sevenDaysAgo := now - int64(7*24*time.Hour)
	thirtyDaysAgo := now - int64(30*24*time.Hour)

	var writes7, writes30 int64
	_ = e.db.QueryRowContext(ctx,
		`SELECT
			COALESCE(COUNT(CASE WHEN occurred_at >= ? THEN 1 END), 0),
			COUNT(1)
		 FROM audit_log
		 WHERE tenant_id = ? AND event_type = ? AND occurred_at >= ?`,
		sevenDaysAgo, tenantID, audit.EventWrite, thirtyDaysAgo).Scan(&writes7, &writes30)

	var mostActive []types.NamespaceActivity
	actRows, err := e.db.QueryContext(ctx,
		`SELECT ns.slug, COUNT(c.id) as cnt
		 FROM claims c
		 JOIN namespaces ns ON ns.id = c.namespace_id
		 WHERE c.tenant_id = ?
		 GROUP BY c.namespace_id
		 ORDER BY cnt DESC LIMIT 5`, tenantID)
	if err == nil {
		defer actRows.Close()
		for actRows.Next() {
			var act types.NamespaceActivity
			if err := actRows.Scan(&act.NamespaceSlug, &act.ClaimCount); err == nil {
				mostActive = append(mostActive, act)
			}
		}
	}

	return &types.TenantSummary{
		TenantID:             tenant.ID,
		TenantName:           tenant.Name,
		TotalClaims:          totalClaims,
		ClaimsByStatus:       statusMap,
		WritesLast7Days:      writes7,
		WritesLast30Days:     writes30,
		MostActiveNamespaces: mostActive,
	}, nil
}

// EnsureLocalWorkspaceTenant returns (or provisions) the default local tenant and an active API key
// for zero-config local MCP hosts (such as Claude Code or Cursor over stdio).
func (e *Engine) EnsureLocalWorkspaceTenant(ctx context.Context) (*types.APIKey, *types.Tenant, error) {
	var tenantID string
	err := e.db.QueryRowContext(ctx, "SELECT id FROM tenants WHERE id = 'local' OR name = 'Local Workspace' LIMIT 1").Scan(&tenantID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, fmt.Errorf("checking local tenant: %w", err)
	}

	if errors.Is(err, sql.ErrNoRows) {
		now := types.NowNano()
		tx, txErr := e.db.BeginTx(ctx, &sql.TxOptions{})
		if txErr != nil {
			return nil, nil, fmt.Errorf("starting transaction: %w", txErr)
		}
		defer tx.Rollback()

		tenantID = "local"
		_, txErr = tx.ExecContext(ctx,
			`INSERT INTO tenants (id, name, status, recall_decay_lambda, max_claims, auto_create_namespaces, created_at, metadata)
			 VALUES (?, 'Local Workspace', 'active', 0.05, 0, 1, ?, '{}')`,
			tenantID, now)
		if txErr != nil {
			return nil, nil, fmt.Errorf("inserting local tenant: %w", txErr)
		}

		defaultNSID := types.NewUUIDv7()
		_, txErr = tx.ExecContext(ctx,
			`INSERT INTO namespaces (id, tenant_id, slug, name, status, access_mode, created_at, updated_at)
			 VALUES (?, ?, 'default', 'Default', 'active', 'tenant_wide', ?, ?)`,
			defaultNSID, tenantID, now, now)
		if txErr != nil {
			return nil, nil, fmt.Errorf("creating default namespace: %w", txErr)
		}

		if err := tx.Commit(); err != nil {
			return nil, nil, fmt.Errorf("committing local tenant: %w", err)
		}
	}

	tenant, err := e.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, nil, err
	}

	if tenant.Status != "active" {
		return nil, nil, fmt.Errorf("%w: local workspace tenant %q is %s", ErrForbidden, tenantID, tenant.Status)
	}

	// Find or issue an active API key for this tenant
	var key types.APIKey
	row := e.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, key_hash, label, status, created_at, expires_at, last_used_at
		 FROM api_keys WHERE tenant_id = ? AND status = 'active' LIMIT 1`, tenantID)
	err = row.Scan(&key.ID, &key.TenantID, &key.KeyHash, &key.Label, &key.Status, &key.CreatedAt, &key.ExpiresAt, &key.LastUsedAt)
	if err == nil {
		return &key, tenant, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, fmt.Errorf("querying api key: %w", err)
	}

	keyResp, err := e.CreateAPIKey(ctx, tenantID, &types.CreateAPIKeyRequest{
		Label: "local-stdio-key",
	}, "system", "system")
	if err != nil {
		return nil, nil, fmt.Errorf("creating local api key: %w", err)
	}

	actor, _, err := e.authSvc.VerifyAPIKey(ctx, keyResp.Key)
	if err != nil {
		return nil, nil, fmt.Errorf("verifying local api key: %w", err)
	}

	return actor, tenant, nil
}

func (e *Engine) ListTenants(ctx context.Context) ([]types.Tenant, error) {
	rows, err := e.db.QueryContext(ctx,
		`SELECT id, name, status, recall_decay_lambda, max_claims, auto_create_namespaces, created_at, metadata
		 FROM tenants ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing tenants: %w", err)
	}
	defer rows.Close()

	var tenants []types.Tenant
	for rows.Next() {
		var t types.Tenant
		var metaJSON string
		if err := rows.Scan(&t.ID, &t.Name, &t.Status, &t.RecallDecayLambda, &t.MaxClaims, &t.AutoCreateNamespaces, &t.CreatedAt, &metaJSON); err != nil {
			return nil, fmt.Errorf("scanning tenant: %w", err)
		}
		if metaJSON != "" && metaJSON != "{}" {
			_ = json.Unmarshal([]byte(metaJSON), &t.Metadata)
		}
		tenants = append(tenants, t)
	}
	return tenants, rows.Err()
}

func (e *Engine) UpdateTenant(ctx context.Context, id string, req *types.UpdateTenantRequest, actorID, actorLabel string) (*types.Tenant, error) {
	t, err := e.GetTenant(ctx, id)
	if err != nil {
		return nil, err
	}

	name := t.Name
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		name = strings.TrimSpace(*req.Name)
	}
	status := t.Status
	if req.Status != nil {
		if *req.Status != "active" && *req.Status != "suspended" {
			return nil, fmt.Errorf("%w: status must be 'active' or 'suspended'", ErrInvalidInput)
		}
		status = *req.Status
	}
	decay := t.RecallDecayLambda
	if req.RecallDecayLambda != nil && *req.RecallDecayLambda > 0 {
		decay = *req.RecallDecayLambda
	}
	maxClaims := t.MaxClaims
	if req.MaxClaims != nil {
		maxClaims = *req.MaxClaims
	}
	autoCreate := t.AutoCreateNamespaces
	if req.AutoCreateNamespaces != nil {
		autoCreate = *req.AutoCreateNamespaces
	}
	meta := t.Metadata
	if req.Metadata != nil {
		meta = req.Metadata
	}
	metaJSON := marshalJSONOr(meta, "{}")

	now := types.NowNano()
	_, err = e.db.ExecContext(ctx,
		`UPDATE tenants SET name = ?, status = ?, recall_decay_lambda = ?, max_claims = ?, auto_create_namespaces = ?, metadata = ?
		 WHERE id = ?`,
		name, status, decay, maxClaims, autoCreate, metaJSON, id)
	if err != nil {
		return nil, fmt.Errorf("updating tenant: %w", err)
	}

	_ = e.auditLogger.Log(ctx, e.db.DB, &types.AuditEvent{
		TenantID:   id,
		EventType:  audit.EventTenantUpdated,
		ActorKeyID: actorID,
		ActorLabel: actorLabel,
		Payload: map[string]any{
			"status":                 status,
			"auto_create_namespaces": autoCreate,
			"max_claims":             maxClaims,
		},
		OccurredAt: now,
	})

	// Invalidate cached identities for this tenant so updated configuration is immediately loaded
	e.authSvc.ClearCache()

	return e.GetTenant(ctx, id)
}

// --- Key Management Operations ---

func (e *Engine) CreateAPIKey(ctx context.Context, tenantID string, req *types.CreateAPIKeyRequest, actorID, actorLabel string) (*types.CreateAPIKeyResponse, error) {
	// Verify tenant exists
	if _, err := e.GetTenant(ctx, tenantID); err != nil {
		return nil, err
	}

	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = "default"
	}

	rawKey, keyHash, err := auth.GenerateAPIKey(tenantID)
	if err != nil {
		return nil, fmt.Errorf("generating api key: %w", err)
	}

	now := types.NowNano()
	keyID := types.NewUUIDv7()

	_, err = e.db.ExecContext(ctx,
		`INSERT INTO api_keys (id, tenant_id, key_hash, label, status, last_used_at, expires_at, created_at)
		 VALUES (?, ?, ?, ?, 'active', 0, ?, ?)`,
		keyID, tenantID, keyHash, label, req.ExpiresAt, now)
	if err != nil {
		return nil, fmt.Errorf("inserting api key: %w", err)
	}

	_ = e.auditLogger.Log(ctx, e.db.DB, &types.AuditEvent{
		TenantID:   tenantID,
		EventType:  audit.EventKeyIssued,
		ActorKeyID: actorID,
		ActorLabel: actorLabel,
		Payload: map[string]any{
			"key_id": keyID,
			"label":  label,
		},
		OccurredAt: now,
	})

	return &types.CreateAPIKeyResponse{
		ID:        keyID,
		TenantID:  tenantID,
		Label:     label,
		Key:       rawKey,
		Status:    "active",
		CreatedAt: now,
		ExpiresAt: req.ExpiresAt,
	}, nil
}

func (e *Engine) RevokeAPIKey(ctx context.Context, tenantID, keyID, actorID, actorLabel string) error {
	res, err := e.db.ExecContext(ctx,
		"UPDATE api_keys SET status = 'revoked' WHERE id = ? AND tenant_id = ?",
		keyID, tenantID)
	if err != nil {
		return fmt.Errorf("revoking api key: %w", err)
	}

	if rows, _ := res.RowsAffected(); rows > 0 {
		e.authSvc.ClearCache()
		_ = e.auditLogger.Log(ctx, e.db.DB, &types.AuditEvent{
			TenantID:   tenantID,
			EventType:  audit.EventKeyRevoked,
			ActorKeyID: actorID,
			ActorLabel: actorLabel,
			Payload: map[string]any{
				"key_id": keyID,
			},
			OccurredAt: types.NowNano(),
		})
	}
	return nil
}

func (e *Engine) ListAPIKeys(ctx context.Context, tenantID string) ([]types.APIKey, error) {
	rows, err := e.db.QueryContext(ctx,
		`SELECT id, tenant_id, label, status, last_used_at, expires_at, created_at
		 FROM api_keys WHERE tenant_id = ? ORDER BY created_at ASC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("listing api keys: %w", err)
	}
	defer rows.Close()

	var keys []types.APIKey
	for rows.Next() {
		var k types.APIKey
		var expiresAt sql.NullInt64
		if err := rows.Scan(&k.ID, &k.TenantID, &k.Label, &k.Status, &k.LastUsedAt, &expiresAt, &k.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning api key: %w", err)
		}
		if expiresAt.Valid {
			k.ExpiresAt = &expiresAt.Int64
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// --- Helper Functions ---

func calculateContentHash(content, tenantID, namespaceID string) string {
	raw := content + "|" + tenantID + "|" + namespaceID
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

func marshalJSONOr(v any, fallback string) string {
	if v == nil {
		return fallback
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fallback
	}
	return string(b)
}

func AuditFilter(tenantID string, nsID, claimID, eventType *string, since, until *int64, limit, offset int) audit.Filter {
	return audit.Filter{
		TenantID:    tenantID,
		NamespaceID: nsID,
		ClaimID:     claimID,
		EventType:   eventType,
		Since:       since,
		Until:       until,
		Limit:       limit,
		Offset:      offset,
	}
}
