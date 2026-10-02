package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

// Filtering happens on the response interceptor, and the caller identity has to
// come out of the request headers. CPA builds the intercept request with
// RequestBody nil, Metadata nil and StatusCode 200, so there is no execution
// metadata to read a caller scope from: the raw Authorization header is the only
// identity that reaches this seam. CPA hands the headers over unfiltered here
// (it only strips X-Forwarded-*, Via, X-Title and X-Stainless-* before this
// point), which is what makes the approach possible at all.
const (
	// authHeader and apiKeyHeader are the two ways a client can present its
	// key. Bearer is what the OpenAI shaped clients send, x-api-key what an
	// Anthropic shaped one sends, and both authenticate against the same
	// api-keys list, so either may be presented to a listing endpoint.
	authHeader   = "Authorization"
	apiKeyHeader = "x-api-key"

	// bearerPrefix is stripped case insensitively: "bearer " and "Bearer " both
	// appear in the wild.
	bearerPrefix = "bearer "
)

// callerScope returns the irreversible namespace CPA uses for a downstream
// credential. It must mirror session.CallerScope byte for byte, because the
// policy table is written in terms of the same digest:
//
//	sha256("cli-proxy-api:caller-scope:v1\x00" + credential) → hex
//
// An absent or unrecognised credential yields "", and "" is never present in a
// policy table, so an unidentified caller is served the untouched list.
func callerScope(credential string) string {
	credential = strings.TrimSpace(credential)
	if credential == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("cli-proxy-api:caller-scope:v1\x00" + credential))
	return hex.EncodeToString(sum[:])
}

// callerCredential pulls the API key out of the request headers. Authorization
// is checked first because it is what every OpenAI shaped client uses; the
// x-api-key fallback covers Anthropic shaped clients, which never send Bearer.
func callerCredential(headers http.Header) string {
	if headers == nil {
		return ""
	}
	if credential := bearerToken(headers.Get(authHeader)); credential != "" {
		return credential
	}
	return strings.TrimSpace(headers.Get(apiKeyHeader))
}

// bearerToken extracts the credential from an Authorization header value.
//
// The scheme is matched case insensitively, because "bearer " and "Bearer " both
// appear in the wild.
//
// Two shapes are deliberately not treated as credentials. A bare scheme word
// ("Bearer" on its own, or "Bearer" followed only by spaces) is a malformed
// header rather than a credential spelled "Bearer", and "Bearer " with nothing
// behind it cannot be split into a scheme and a token at all. Hashing either
// would produce a scope that can never appear in a policy table, which is
// harmless, but reporting it as an identity would make an unauthenticated
// request look like an authenticated one in the panel.
func bearerToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) < len(bearerPrefix) {
		// Too short to carry a scheme. Either it is the bare word "Bearer" or a
		// scheme-less token; only the latter is a credential.
		if strings.EqualFold(value, strings.TrimSpace(bearerPrefix)) {
			return ""
		}
		return value
	}
	if strings.EqualFold(value[:len(bearerPrefix)], bearerPrefix) {
		return strings.TrimSpace(value[len(bearerPrefix):])
	}
	return value
}
