// Copyright (c) CircleCI
// SPDX-License-Identifier: MPL-2.0

package circleci

import "context"

// Routes for OTLP exporters. The collection is scoped by an org-id *query*
// parameter rather than a path segment, unlike most v2 collections.
//
// These are v2 but they are NOT served on CircleCI Server. The public API only
// proxies /api/v2/otel through to a backend service, and CircleCI Server does
// not deploy that backend or route to it at all: everything else under /api
// falls through to the core v2 API backend, which has no such route. Callers
// must gate on Client.IsCloud. Compare urlOrbAllowListRoute, which is v2 *and*
// owned by that core backend, so Server does serve it.
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
//
// [NET] Confirmed against a real Cloud organization (five bare-endpoint
// creates, then a sixth): the sixth answers HTTP 422, and the response body is
// the unhelpful `{"message":"Internal server error."}` — not a message that
// names the limit. This package's own diagnostic (see CreateOTelExporter's
// caller in the resource) supplies the explanation instead of trusting the
// API's text.
const OTelExporterLimit = 5

// OTelRedactedHeaderValue is what the API substitutes for a header value it will
// not disclose. Header values are encrypted at rest and every read answers with
// this placeholder, so a value that was sent can never be read back.
//
// Verified against the API: it replaces every *value* in the map and
// leaves the *names* untouched, so a header added or removed elsewhere is
// observable while a changed value is not. The same redaction is applied to the
// create response, not only to reads, so nothing in this package ever sees a
// header value it did not send.
const OTelRedactedHeaderValue = "xxxx"

// OTelExporterHeaderLimit is the maximum number of headers one exporter may
// carry. [NET] Confirmed against a real Cloud organization: a create with six
// headers is rejected 400 `{"message":"Too many headers provided."}`, and a
// create with one header named "grpc-foo" is rejected 400
// `{"message":"Header name is reserved."}`.
//
// The API also caps a header name at 64 characters and a value at 1024,
// rejects names beginning with ":", and rejects the HTTP and gRPC names it
// manages itself (content-type, content-length, connection, keep-alive, te,
// trailers, transfer-encoding, upgrade) — those specifics are carried over
// from the internal route table this client was built from and have NOT been
// exercised against the real API; only the header count and the "grpc-"
// prefix above have. None of it is enforced here regardless: the API's
// message is clearer than anything this package could synthesize.
const OTelExporterHeaderLimit = 5

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
	// Endpoint is where spans are sent. See CreateOTelExporterRequest for the two
	// forms the API accepts.
	Endpoint string `json:"endpoint"`
	// Protocol is the OTLP transport, either OTelProtocolGRPC or
	// OTelProtocolHTTP.
	Protocol string `json:"protocol"`
	// Insecure disables transport security to the endpoint.
	Insecure bool `json:"insecure"`
	// Headers are extra headers sent with each export. Values read back from the
	// API are always OTelRedactedHeaderValue, never the value that was sent. The
	// key is omitted entirely when an exporter has no headers, so this decodes as
	// a nil map rather than an empty one.
	Headers map[string]string `json:"headers"`
	// Issues lists validation problems CircleCI has detected with this
	// configuration, such as an endpoint that no longer resolves. It is absent
	// when there are none, and absent from the create response altogether: only
	// the list route computes it.
	Issues []string `json:"issues"`
}

// CreateOTelExporterRequest is the POST body. There is no update route, so this
// is the only way an exporter's configuration is ever written.
//
// Endpoint has TWO accepted forms, and the published OpenAPI schema describes
// only one of them. The schema says "Don't include https:// or grpc://. Just the
// hostname and port are required", but the service that actually validates the
// request accepts either:
//
//   - a bare "host:port" — the port is mandatory in this form, and must be
//     1-65535; or
//   - an "http://" or "https://" URL, optionally with a port and a path, which
//     is only valid together with Protocol == OTelProtocolHTTP. A URL endpoint
//     with OTelProtocolGRPC is rejected with "protocol must be 'http' when
//     endpoint starts with http:// or https://".
//
// No other scheme is accepted: "grpc://host:4317" parses as neither form and is
// rejected as malformed. The host must also resolve, and must not resolve to a
// private, loopback, multicast or link-local address — the service refuses to be
// pointed at anything inside a network it can reach. [NET] The loopback case is
// confirmed: "127.0.0.1:4317" against a real organization is rejected 400
// `{"message":"Invalid endpoint hostname."}`. Private, multicast and
// link-local are carried over from the internal route table and have not
// been separately exercised.
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
//
// A caller without the "manage-org" permission on orgID gets HTTP 404 "Org not
// found", exactly as an organization that does not exist does — the route
// deliberately conflates the two. That 404 satisfies IsNotFound, so a caller
// using this to detect drift must NOT treat every IsNotFound as "the exporter is
// gone": see GetOTelExporter.
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
// exporters and selects from them, reporting the ErrNotFound *sentinel* when none
// matches. That is what lets a resource distinguish an exporter deleted outside
// Terraform from an API failure.
//
// The distinction is load-bearing rather than cosmetic. Only the sentinel means
// "this exporter no longer exists": an HTTP 404 from the listing means the
// organization is missing or the token cannot manage it, and dropping the
// resource from state on that would have Terraform create a second exporter
// against a limit of OTelExporterLimit. Test with errors.Is(err, ErrNotFound),
// not IsNotFound, wherever that difference matters.
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
//
// It answers 201 with the new exporter, its header values already replaced by
// OTelRedactedHeaderValue. An organization already holding OTelExporterLimit
// exporters gets HTTP 422, not 400, and a caller who cannot manage the
// organization gets 404 "Org not found".
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
