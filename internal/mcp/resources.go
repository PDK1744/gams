package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/PDK1744/gams/internal/engine"
	"github.com/PDK1744/gams/pkg/types"
)

// GetResourceDefinitions returns statically known resources.
func GetResourceDefinitions(tenant *types.Tenant) []Resource {
	descTenant := "Aggregate memory statistics across all namespaces"
	return []Resource{
		{
			URI:         fmt.Sprintf("gams://tenants/%s/summary", tenant.ID),
			Name:        "Tenant Summary",
			Description: &descTenant,
			MIMEType:    "application/json",
		},
	}
}

// GetResourceTemplates returns parameterized URI templates for browsing memory entities.
func GetResourceTemplates() []ResourceTemplate {
	descClaim := "Current state and metadata of a single claim"
	descNamespace := "Namespace metadata, access mode, and live claim volume statistics"
	descTenant := "Tenant aggregate claim and write activity summary"

	return []ResourceTemplate{
		{
			URITemplate: "gams://claims/{claim_id}",
			Name:        "Claim by ID",
			Description: &descClaim,
			MIMEType:    "application/json",
		},
		{
			URITemplate: "gams://namespaces/{namespace_slug}",
			Name:        "Namespace Metadata & Stats",
			Description: &descNamespace,
			MIMEType:    "application/json",
		},
		{
			URITemplate: "gams://tenants/{tenant_id}/summary",
			Name:        "Tenant Summary",
			Description: &descTenant,
			MIMEType:    "application/json",
		},
	}
}

// ReadResource fetches the resource content for a given URI.
func ReadResource(ctx context.Context, eng *engine.Engine, actor *types.APIKey, tenant *types.Tenant, uriStr string) (*ReadResourceResult, error) {
	u, err := url.Parse(uriStr)
	if err != nil || u.Scheme != "gams" {
		return nil, fmt.Errorf("invalid resource URI %q: must start with gams://", uriStr)
	}

	pathParts := strings.Split(strings.Trim(u.Path, "/"), "/")
	resourceType := u.Host
	if resourceType == "" && len(pathParts) > 0 {
		resourceType = pathParts[0]
		pathParts = pathParts[1:]
	}

	switch resourceType {
	case "claims":
		if len(pathParts) != 1 || pathParts[0] == "" {
			return nil, fmt.Errorf("invalid claim URI format; expected gams://claims/{claim_id}")
		}
		claimID := pathParts[0]
		insp, err := eng.Inspect(ctx, actor, tenant, claimID)
		if err != nil {
			return nil, fmt.Errorf("reading claim resource: %w", err)
		}
		b, err := json.Marshal(insp.Claim)
		if err != nil {
			return nil, err
		}
		return &ReadResourceResult{
			Contents: []ResourceContent{
				{
					URI:      uriStr,
					MIMEType: "application/json",
					Text:     string(b),
				},
			},
		}, nil

	case "namespaces":
		if len(pathParts) != 1 || pathParts[0] == "" {
			return nil, fmt.Errorf("invalid namespace URI format; expected gams://namespaces/{slug}")
		}
		slug := pathParts[0]
		ns, err := eng.GetNamespace(ctx, tenant.ID, slug)
		if err != nil {
			return nil, fmt.Errorf("reading namespace: %w", err)
		}
		if err := eng.Auth().CheckNamespaceAccess(ctx, actor, ns, "read"); err != nil {
			return nil, fmt.Errorf("access denied to namespace %q: %w", slug, err)
		}
		detail, err := eng.GetNamespaceDetail(ctx, tenant.ID, slug)
		if err != nil {
			return nil, fmt.Errorf("reading namespace detail: %w", err)
		}
		b, err := json.Marshal(detail)
		if err != nil {
			return nil, err
		}
		return &ReadResourceResult{
			Contents: []ResourceContent{
				{
					URI:      uriStr,
					MIMEType: "application/json",
					Text:     string(b),
				},
			},
		}, nil

	case "tenants":
		if len(pathParts) != 2 || pathParts[1] != "summary" {
			return nil, fmt.Errorf("invalid tenant URI format; expected gams://tenants/{tenant_id}/summary")
		}
		tenantID := pathParts[0]
		if tenantID != tenant.ID {
			return nil, fmt.Errorf("unauthorized access to tenant %q summary", tenantID)
		}
		summary, err := eng.GetTenantSummary(ctx, tenant.ID)
		if err != nil {
			return nil, fmt.Errorf("generating tenant summary: %w", err)
		}
		b, err := json.Marshal(summary)
		if err != nil {
			return nil, err
		}
		return &ReadResourceResult{
			Contents: []ResourceContent{
				{
					URI:      uriStr,
					MIMEType: "application/json",
					Text:     string(b),
				},
			},
		}, nil

	default:
		return nil, fmt.Errorf("unsupported resource type: %q", resourceType)
	}
}
