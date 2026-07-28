// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Routes for OTLP exporters. The collection is scoped by an org-id *query*
// parameter rather than a path segment, unlike most v2 collections.
const (
	otelExportersRoute = "/otel/exporters"
	otelExporterRoute  = "/otel/exporters/%s"
)

// The OTLP transports the API accepts.
const (
	// OTelProtocolGRPC exports over OTLP/gRPC.
	OTelProtocolGRPC = "grpc"
	// OTelProtocolHTTP exports over OTLP/HTTP.
	OTelProtocolHTTP = "http"
)

// OTelExporterLimit is the maximum number of exporter configurations CircleCI
// allows per organization. Creating a sixth is rejected.
const OTelExporterLimit = 5

// OTelRedactedHeaderValue is what the API substitutes for a header value it will
// not disclose. Header values are encrypted at rest and every read answers with
// this placeholder, so a value that was sent can never be read back.
const OTelRedactedHeaderValue = "xxxx"

// OTelExporter is an OTLP exporter configuration, as returned by
// GET and POST /api/v2/otel/exporters.
//
// The API flags these endpoints experimental, so both the routes and this shape
// may change without a v2 deprecation cycle.
type OTelExporter struct {
	// ID is the exporter's identifier, assigned by CircleCI.
	ID string `json:"id"`
	// OrgID is the organization that owns the exporter.
	OrgID string `json:"org_id"`
	// Endpoint is the host:port spans are sent to, with no scheme.
	Endpoint string `json:"endpoint"`
	// Protocol is the OTLP transport, either OTelProtocolGRPC or
	// OTelProtocolHTTP.
	Protocol string `json:"protocol"`
	// Insecure disables transport security to the endpoint.
	Insecure bool `json:"insecure"`
	// Headers are extra headers sent with each export. Values read back from the
	// API are always OTelRedactedHeaderValue, never the value that was sent.
	Headers map[string]string `json:"headers"`
	// Issues lists validation problems CircleCI has detected with this
	// configuration, such as an endpoint that no longer resolves. It is absent
	// when there are none.
	Issues []string `json:"issues"`
}

// CreateOTelExporterRequest is the POST body. There is no update route, so this
// is the only way an exporter's configuration is ever written.
type CreateOTelExporterRequest struct {
	OrgID    string            `json:"org_id"`
	Endpoint string            `json:"endpoint"`
	Protocol string            `json:"protocol"`
	Insecure bool              `json:"insecure"`
	Headers  map[string]string `json:"headers,omitempty"`
}

// ListOTelExporters returns every OTLP exporter configured for an organization.
//
// The response is a bare JSON array with no pagination envelope, and the limit
// of OTelExporterLimit per organization means there is never more than one page.
func (c *Client) ListOTelExporters(ctx context.Context, orgID string) ([]OTelExporter, error) {
	var exporters []OTelExporter

	if err := c.GetV2(ctx, otelExportersRoute, &exporters, Query("org-id", orgID)); err != nil {
		return nil, err
	}

	return exporters, nil
}

// GetOTelExporter returns one organization's exporter by ID.
//
// The API has no route for a single exporter, so this lists the organization's
// exporters and selects from them, reporting ErrNotFound when none matches. That
// is what lets a resource distinguish an exporter deleted outside Terraform from
// an API failure.
func (c *Client) GetOTelExporter(ctx context.Context, orgID, id string) (*OTelExporter, error) {
	exporters, err := c.ListOTelExporters(ctx, orgID)
	if err != nil {
		return nil, err
	}

	for i := range exporters {
		if exporters[i].ID == id {
			return &exporters[i], nil
		}
	}

	return nil, ErrNotFound
}

// CreateOTelExporter creates an OTLP exporter configuration.
func (c *Client) CreateOTelExporter(ctx context.Context, req CreateOTelExporterRequest) (*OTelExporter, error) {
	var exporter OTelExporter

	if err := c.PostV2(ctx, otelExportersRoute, req, &exporter); err != nil {
		return nil, err
	}

	return &exporter, nil
}

// DeleteOTelExporter deletes an OTLP exporter configuration by ID. The route
// takes no organization: the ID alone identifies the exporter.
func (c *Client) DeleteOTelExporter(ctx context.Context, id string) error {
	return c.DeleteV2(ctx, otelExporterRoute, RouteParams(id))
}
