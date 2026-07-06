package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sud0x0/go-api-template/internal/config"
	"github.com/sud0x0/go-api-template/internal/shared"
	"github.com/sud0x0/go-api-template/internal/shared/logger"
)

// errUndefinedDecision is returned when OPA responds 200 but with no "result"
// key: an undefined decision (no matching rule and no default). It is a DENY.
var errUndefinedDecision = errors.New("opa returned an undefined decision (no result)")

// opaStatusError reports a non-200 response from OPA. The status is bounded
// (an HTTP code), so it is safe to log.
type opaStatusError struct{ status int }

func (e *opaStatusError) Error() string {
	return fmt.Sprintf("opa returned non-200 status %d", e.status)
}

// This file implements LAYER 2 of the two-layer auth model: AUTHORISATION.
//
// After authentication (auth_middleware.go), this middleware asks an Open Policy
// Agent (OPA) sidecar, over its REST Data API, whether the authenticated
// principal may perform the requested action on the requested resource, and
// enforces the answer. This is FUNCTION-LEVEL authorisation only.
//
// It does NOT replace the repository's WHERE user_id = $N SQL scoping. That
// remains the ROW-OWNERSHIP layer (defence in depth): OPA decides "may this role
// call this endpoint", the SQL decides "may this user see this row". Collapsing
// the two (e.g. pushing row filtering into OPA) is a mistake; keep them split
// (see the wiring comment in cmd/api/main.go and .claude/rules/decisions.md).
//
// Communication is OPA's REST Data API over HTTP, NOT gRPC and NOT the Envoy
// external-authz variant: the app calls OPA directly, so the standard
// openpolicyagent/opa image suffices and there is no Envoy in the path.

// opaRequest is the OPA REST Data API request body: {"input": {...}}.
type opaRequest struct {
	Input opaInput `json:"input"`
}

// opaInput is the decision input. Every field is BOUNDED: subject is an
// internal UUID, roles are internal role names, action is a fixed verb, resource
// is the chi ROUTE PATTERN (never the raw path), and method is the HTTP method.
// Using the route pattern (e.g. /api/v1/logs/{id}) keeps the policy input a
// small fixed set, matching the metrics cardinality rule.
type opaInput struct {
	Subject  string   `json:"subject"`
	Roles    []string `json:"roles"`
	Action   string   `json:"action"`
	Resource string   `json:"resource"`
	Method   string   `json:"method"`
}

// opaResponse is the OPA REST Data API response. Result is a POINTER so a body
// with no "result" key (an undefined decision) decodes to nil and is treated as
// a DENY, not a false-y allow, part of failing closed.
type opaResponse struct {
	Result *bool `json:"result"`
}

// OPAAuthorizer is the function-level authorisation middleware. Build it once at
// startup with NewOPAAuthorizer; the per-request Handler POSTs the decision
// input to the OPA sidecar and enforces the boolean result.
type OPAAuthorizer struct {
	client      *http.Client
	decisionURL string
	timeout     time.Duration
	log         logger.Logger
}

// NewOPAAuthorizer builds the authorizer from cfg. The decision URL is
// cfg.URL + "/" + cfg.DecisionPath with any stray slashes trimmed, e.g.
// http://opa:8181/v1/data/api/authz/allow.
func NewOPAAuthorizer(cfg config.OPAConfig, log logger.Logger) *OPAAuthorizer {
	decisionURL := strings.TrimRight(cfg.URL, "/") + "/" + strings.TrimLeft(cfg.DecisionPath, "/")
	return &OPAAuthorizer{
		// No per-client Timeout: each request is bounded by a context deadline in
		// Handler instead, so a disconnecting client also cancels the OPA call.
		client:      &http.Client{},
		decisionURL: decisionURL,
		timeout:     cfg.Timeout,
		log:         log,
	}
}

// Handler is the net/http middleware. It builds the decision input, asks OPA,
// and calls next ONLY on an explicit allow. Every failure path (a build error,
// a transport error, a timeout, a non-200 status, an undecodable body, or a
// missing/false result) DENIES with a 403 in the JSON envelope.
//
// FAIL CLOSED is the security-critical default (source: OPA's own
// API-authorization guidance). An authorisation check that fails open turns any
// OPA outage into a full authorisation bypass; failing closed turns it into a
// visible 403 instead. Only result==true allows.
func (a *OPAAuthorizer) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		subject, _ := shared.UserIDFromContext(r.Context())
		input := opaInput{
			Subject:  subject,
			Roles:    rolesFromContext(r.Context()),
			Action:   methodToAction(r.Method),
			Resource: routePattern(r),
			Method:   r.Method,
		}

		allowed, err := a.decide(r.Context(), input)
		if err != nil {
			// Deny without leaking OPA internals to the client; log the cause for
			// operators. The 403 body is a fixed bounded string.
			logger.FromContext(r.Context(), a.log).LogWarn("authorisation denied: OPA decision unavailable",
				"error", err.Error(), "resource", input.Resource, "action", input.Action)
			shared.WriteJSONError(w, http.StatusForbidden, shared.ErrTypeForbidden, "forbidden")
			return
		}
		if !allowed {
			shared.WriteJSONError(w, http.StatusForbidden, shared.ErrTypeForbidden, "forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// decide POSTs the input to OPA and returns the boolean decision. It returns a
// non-nil error for EVERY non-allow-decidable outcome so the caller fails
// closed. A clean (result != nil) response returns (*result, nil).
func (a *OPAAuthorizer) decide(ctx context.Context, input opaInput) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	body, err := json.Marshal(opaRequest{Input: input})
	if err != nil {
		return false, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.decisionURL, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return false, &opaStatusError{status: resp.StatusCode}
	}

	var decision opaResponse
	if err := json.NewDecoder(resp.Body).Decode(&decision); err != nil {
		return false, err
	}
	if decision.Result == nil {
		// Undefined decision (no matching rule, no default). Fail closed.
		return false, errUndefinedDecision
	}
	return *decision.Result, nil
}

// methodToAction maps an HTTP method to the fixed action verb the policy reasons
// about. The set is bounded (matching the metrics rule); an unknown method maps
// to the lowercased method itself so the policy can still deny it explicitly.
func methodToAction(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead:
		return "read"
	case http.MethodPost:
		return "create"
	case http.MethodPut, http.MethodPatch:
		return "update"
	case http.MethodDelete:
		return "delete"
	default:
		return strings.ToLower(method)
	}
}

// routePattern returns the chi route pattern for the request (e.g.
// /api/v1/logs/{id}), NOT the raw URL path. The pattern is a bounded value from
// chi's compiled routes, so it is safe as policy input and as a log field. If no
// pattern is resolved (should not happen for a matched business route), it
// returns "unknown" so the policy still receives a defined, bounded value.
func routePattern(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		if p := rc.RoutePattern(); p != "" {
			return p
		}
	}
	return "unknown"
}
