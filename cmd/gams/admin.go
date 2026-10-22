package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/PDK1744/gams/internal/auth"
	"github.com/PDK1744/gams/internal/config"
	"github.com/PDK1744/gams/internal/db"
	"github.com/PDK1744/gams/internal/engine"
	"github.com/PDK1744/gams/pkg/types"
)

func runAdmin(args []string) error {
	if len(args) == 0 {
		printAdminUsage()
		return nil
	}

	switch args[0] {
	case "help", "--help", "-h":
		printAdminUsage()
		return nil
	case "claims":
		return runAdminClaims(args[1:])
	case "audit":
		return runAdminAudit(args[1:])
	default:
		return fmt.Errorf("unknown admin command %q; run 'gams admin help' for usage", args[0])
	}
}

func printAdminUsage() {
	fmt.Print(`GAMS Admin CLI

Usage:
  gams admin [command] [subcommand] [options]

Commands:
  claims inspect <claim-id>   Inspect a claim's full governance history, versions, evidence, and audit trail
  claims export --tenant <id> Export full claims snapshot (all versions, evidence) as JSONL with schema_version: 1.0
  audit export --tenant <id>  Export chronological audit log as JSONL with mandatory schema_version: 1.0

Options:
  --tenant <id>               Tenant UUID (required for export commands)
  --namespace <slug>          Optional namespace slug filter
  --config <path>             Path to gams.toml config file (default: gams.toml)
  --format <text|json|jsonl>  Output format: text | json | jsonl (default: text for inspect, jsonl for export)
  --server <url>              GAMS server base URL (optional; if omitted, queries local database directly)
  --admin-key <key>           Admin API key for remote server queries
`)
}

func runAdminClaims(args []string) error {
	if len(args) == 0 {
		return errors.New("missing claims subcommand; e.g. 'gams admin claims inspect <claim-id>' or 'gams admin claims export --tenant <id>'")
	}

	switch args[0] {
	case "inspect":
		return runAdminClaimsInspect(args[1:])
	case "export":
		return runAdminClaimsExport(args[1:])
	default:
		return fmt.Errorf("unknown claims subcommand %q; see 'gams admin help'", args[0])
	}
}

func runAdminAudit(args []string) error {
	if len(args) == 0 {
		return errors.New("missing audit subcommand; e.g. 'gams admin audit export --tenant <id>'")
	}

	switch args[0] {
	case "export":
		return runAdminAuditExport(args[1:])
	default:
		return fmt.Errorf("unknown audit subcommand %q; see 'gams admin help'", args[0])
	}
}

func runAdminClaimsInspect(args []string) error {
	fs := flag.NewFlagSet("claims inspect", flag.ContinueOnError)
	configPath := fs.String("config", "gams.toml", "path to configuration file")
	format := fs.String("format", "text", "output format: text | json")
	serverURL := fs.String("server", "", "GAMS server base URL (optional; reads local DB if empty)")
	adminKey := fs.String("admin-key", "", "GAMS admin key (optional)")

	// Reorder args so flags precede positional arguments, permitting flags after <claim-id>
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			if !strings.Contains(arg, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				flags = append(flags, args[i])
			}
		} else {
			positional = append(positional, arg)
		}
	}
	reordered := append(flags, positional...)

	if err := fs.Parse(reordered); err != nil {
		return err
	}

	extraArgs := fs.Args()
	if len(extraArgs) < 1 {
		return errors.New("missing required argument: <claim-id>\nUsage: gams admin claims inspect <claim-id> [options]")
	}
	claimID := extraArgs[0]

	var resp *types.InspectResponse
	var err error

	if *serverURL != "" {
		resp, err = inspectViaHTTP(*serverURL, *adminKey, claimID)
	} else {
		resp, err = inspectViaDirectDB(*configPath, claimID)
	}
	if err != nil {
		return err
	}

	if strings.ToLower(*format) == "json" {
		out, err := json.MarshalIndent(resp, "", "  ")
		if err != nil {
			return fmt.Errorf("formatting json: %w", err)
		}
		fmt.Println(string(out))
		return nil
	}

	renderInspectHumanReadable(os.Stdout, resp)
	return nil
}

func inspectViaDirectDB(configPath string, claimID string) (*types.InspectResponse, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("loading config %q: %w", configPath, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	database, err := db.Open(ctx, cfg.Data.Path)
	if err != nil {
		return nil, fmt.Errorf("opening database at %q: %w", cfg.Data.Path, err)
	}
	defer database.Close()

	authSvc := auth.NewService(database.DB, cfg.Server.AdminKey, 300*time.Second)
	eng := engine.New(database, authSvc, cfg, Version)

	var tenantID string
	err = database.DB.QueryRowContext(ctx, "SELECT tenant_id FROM claims WHERE id = ?", claimID).Scan(&tenantID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("claim %q not found", claimID)
		}
		return nil, fmt.Errorf("locating claim: %w", err)
	}

	tenant, err := eng.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("resolving tenant: %w", err)
	}

	return eng.Inspect(ctx, nil, tenant, claimID)
}

func inspectViaHTTP(serverURL, adminKey, claimID string) (*types.InspectResponse, error) {
	url := strings.TrimRight(serverURL, "/") + "/v1/claims/" + claimID
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating http request: %w", err)
	}
	if adminKey != "" {
		req.Header.Set("Authorization", "Bearer "+adminKey)
	} else if envKey := os.Getenv("GAMS_ADMIN_KEY"); envKey != "" {
		req.Header.Set("Authorization", "Bearer "+envKey)
	} else if envKey := os.Getenv("GAMS_API_KEY"); envKey != "" {
		req.Header.Set("Authorization", "Bearer "+envKey)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connecting to server %s: %w", serverURL, err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if res.StatusCode != http.StatusOK {
		var errObj struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &errObj) == nil && errObj.Error != "" {
			return nil, fmt.Errorf("server error (%d): %s", res.StatusCode, errObj.Error)
		}
		return nil, fmt.Errorf("server returned HTTP %d: %s", res.StatusCode, string(body))
	}

	var inspectResp types.InspectResponse
	if err := json.Unmarshal(body, &inspectResp); err != nil {
		return nil, fmt.Errorf("decoding inspect response: %w", err)
	}
	return &inspectResp, nil
}

func renderInspectHumanReadable(w io.Writer, resp *types.InspectResponse) {
	c := resp.Claim
	fmt.Fprintf(w, "================================================================================\n")
	fmt.Fprintf(w, " GAMS CLAIM LINEAGE & GOVERNANCE REPORT\n")
	fmt.Fprintf(w, "================================================================================\n")
	fmt.Fprintf(w, "Claim ID:        %s\n", c.ID)
	fmt.Fprintf(w, "Tenant ID:       %s\n", c.TenantID)
	fmt.Fprintf(w, "Namespace ID:    %s\n", c.NamespaceID)
	fmt.Fprintf(w, "Current Version: v%d\n", c.CurrentVersion)
	fmt.Fprintf(w, "Status:          [%s]\n", strings.ToUpper(c.Status))
	if c.ContradictedBy != nil {
		fmt.Fprintf(w, "Contradicted By: %s\n", *c.ContradictedBy)
	}
	if c.ForgottenAt != nil {
		fmt.Fprintf(w, "Forgotten At:    %s\n", types.FormatISO8601(*c.ForgottenAt))
	}
	if c.ForgottenReason != nil {
		fmt.Fprintf(w, "Forgotten Reason:%s\n", *c.ForgottenReason)
	}
	fmt.Fprintf(w, "Confidence:      %.2f\n", c.Confidence)
	writerLabel := c.WriterLabel
	if writerLabel == "" {
		writerLabel = c.WriterID
	}
	fmt.Fprintf(w, "Writer:          %s (%s)\n", writerLabel, c.WriterID)
	fmt.Fprintf(w, "Created At:      %s\n", types.FormatISO8601(c.CreatedAt))
	fmt.Fprintf(w, "Updated At:      %s\n", types.FormatISO8601(c.UpdatedAt))
	if len(c.Tags) > 0 {
		fmt.Fprintf(w, "Tags:            [%s]\n", strings.Join(c.Tags, ", "))
	}

	fmt.Fprintf(w, "\n--------------------------------------------------------------------------------\n")
	fmt.Fprintf(w, " CURRENT CONTENT\n")
	fmt.Fprintf(w, "--------------------------------------------------------------------------------\n")
	fmt.Fprintf(w, "%s\n", c.Content)

	fmt.Fprintf(w, "\n--------------------------------------------------------------------------------\n")
	fmt.Fprintf(w, " VERSION HISTORY (%d versions)\n", len(resp.Versions))
	fmt.Fprintf(w, "--------------------------------------------------------------------------------\n")
	for _, v := range resp.Versions {
		wLabel := v.WriterLabel
		if wLabel == "" {
			wLabel = v.WriterID
		}
		contra := "none"
		if len(v.Contradicts) > 0 {
			contra = fmt.Sprintf("[%s]", strings.Join(v.Contradicts, ", "))
		}
		chgReason := "none"
		if v.ChangeReason != nil && *v.ChangeReason != "" {
			chgReason = *v.ChangeReason
		}
		fmt.Fprintf(w, "• v%d | Written: %s | Writer: %s | Confidence: %.2f\n",
			v.Version, types.FormatISO8601(v.WrittenAt), wLabel, v.Confidence)
		fmt.Fprintf(w, "    Contradicts:   %s\n", contra)
		fmt.Fprintf(w, "    Change Reason: %s\n", chgReason)
		fmt.Fprintf(w, "    Content:       %s\n", v.Content)
	}

	fmt.Fprintf(w, "\n--------------------------------------------------------------------------------\n")
	fmt.Fprintf(w, " EVIDENCE (%d items)\n", len(resp.Evidence))
	fmt.Fprintf(w, "--------------------------------------------------------------------------------\n")
	if len(resp.Evidence) == 0 {
		fmt.Fprintf(w, "  (No evidence records linked)\n")
	} else {
		for _, ev := range resp.Evidence {
			fmt.Fprintf(w, "• [v%d] [%s] %s\n", ev.ClaimVersion, ev.EvidenceType, ev.URI)
			fmt.Fprintf(w, "    Description: %s\n", ev.Description)
			if ev.CapturedAt > 0 {
				fmt.Fprintf(w, "    Captured:    %s\n", types.FormatISO8601(ev.CapturedAt))
			}
		}
	}

	fmt.Fprintf(w, "\n--------------------------------------------------------------------------------\n")
	fmt.Fprintf(w, " GOVERNANCE AUDIT TRAIL (%d events)\n", len(resp.Audit))
	fmt.Fprintf(w, "--------------------------------------------------------------------------------\n")
	if len(resp.Audit) == 0 {
		fmt.Fprintf(w, "  (No audit events recorded)\n")
	} else {
		for _, a := range resp.Audit {
			verStr := "-"
			if a.ClaimVersion != nil {
				verStr = fmt.Sprintf("v%d", *a.ClaimVersion)
			}
			actor := a.ActorLabel
			if actor == "" {
				actor = a.ActorKeyID
			}
			fmt.Fprintf(w, "• %s | EVENT: %-12s | Version: %-4s | Actor: %s\n",
				types.FormatISO8601(a.OccurredAt), a.EventType, verStr, actor)
			if len(a.Payload) > 0 {
				payloadBytes, _ := json.Marshal(a.Payload)
				fmt.Fprintf(w, "    Payload: %s\n", string(payloadBytes))
			}
		}
	}
	fmt.Fprintf(w, "================================================================================\n")
}

func runAdminAuditExport(args []string) error {
	fs := flag.NewFlagSet("audit export", flag.ContinueOnError)
	tenantID := fs.String("tenant", "", "tenant UUID (required)")
	namespace := fs.String("namespace", "", "namespace slug (optional)")
	format := fs.String("format", "jsonl", "output format (default: jsonl)")
	configPath := fs.String("config", "gams.toml", "path to configuration file")
	serverURL := fs.String("server", "", "GAMS server base URL (optional; queries local DB if empty)")
	adminKey := fs.String("admin-key", "", "GAMS admin key (optional)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *tenantID == "" {
		return errors.New("missing required flag: --tenant <id>\nUsage: gams admin audit export --tenant <id> [--namespace <slug>] [--format jsonl]")
	}

	var nsSlug *string
	if *namespace != "" {
		nsSlug = namespace
	}

	if *serverURL != "" {
		path := fmt.Sprintf("/admin/tenants/%s/audit/export?format=%s", *tenantID, *format)
		if nsSlug != nil {
			path += "&namespace=" + *nsSlug
		}
		return exportViaHTTP(*serverURL, *adminKey, path)
	}

	return exportAuditViaDirectDB(*configPath, *tenantID, nsSlug)
}

func runAdminClaimsExport(args []string) error {
	fs := flag.NewFlagSet("claims export", flag.ContinueOnError)
	tenantID := fs.String("tenant", "", "tenant UUID (required)")
	namespace := fs.String("namespace", "", "namespace slug (optional)")
	configPath := fs.String("config", "gams.toml", "path to configuration file")
	serverURL := fs.String("server", "", "GAMS server base URL (optional; queries local DB if empty)")
	adminKey := fs.String("admin-key", "", "GAMS admin key (optional)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *tenantID == "" {
		return errors.New("missing required flag: --tenant <id>\nUsage: gams admin claims export --tenant <id> [--namespace <slug>]")
	}

	var nsSlug *string
	if *namespace != "" {
		nsSlug = namespace
	}

	if *serverURL != "" {
		path := fmt.Sprintf("/admin/tenants/%s/claims/export", *tenantID)
		if nsSlug != nil {
			path += "?namespace=" + *nsSlug
		}
		return exportViaHTTP(*serverURL, *adminKey, path)
	}

	return exportClaimsViaDirectDB(*configPath, *tenantID, nsSlug)
}

func exportAuditViaDirectDB(configPath, tenantID string, nsSlug *string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading config %q: %w", configPath, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	database, err := db.Open(ctx, cfg.Data.Path)
	if err != nil {
		return fmt.Errorf("opening database at %q: %w", cfg.Data.Path, err)
	}
	defer database.Close()

	authSvc := auth.NewService(database.DB, cfg.Server.AdminKey, 300*time.Second)
	eng := engine.New(database, authSvc, cfg, Version)

	return eng.ExportAudit(ctx, tenantID, nsSlug, os.Stdout)
}

func exportClaimsViaDirectDB(configPath, tenantID string, nsSlug *string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading config %q: %w", configPath, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	database, err := db.Open(ctx, cfg.Data.Path)
	if err != nil {
		return fmt.Errorf("opening database at %q: %w", cfg.Data.Path, err)
	}
	defer database.Close()

	authSvc := auth.NewService(database.DB, cfg.Server.AdminKey, 300*time.Second)
	eng := engine.New(database, authSvc, cfg, Version)

	return eng.ExportClaims(ctx, tenantID, nsSlug, os.Stdout)
}

func exportViaHTTP(serverURL, adminKey, path string) error {
	url := strings.TrimRight(serverURL, "/") + path
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("creating export http request: %w", err)
	}
	if adminKey != "" {
		req.Header.Set("Authorization", "Bearer "+adminKey)
	} else if envKey := os.Getenv("GAMS_ADMIN_KEY"); envKey != "" {
		req.Header.Set("Authorization", "Bearer "+envKey)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to server %s: %w", serverURL, err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		var errObj struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &errObj) == nil && errObj.Error != "" {
			return fmt.Errorf("server error (%d): %s", res.StatusCode, errObj.Error)
		}
		return fmt.Errorf("server returned HTTP %d: %s", res.StatusCode, string(body))
	}

	_, err = io.Copy(os.Stdout, res.Body)
	return err
}
