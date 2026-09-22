// Package main implements model-order, a thin CLIProxyAPI plugin that gives the
// model listing endpoints a stable, configured order.
//
// CPA builds /v1/models by ranging over a map keyed by model ID and never sorts
// the result, so the served order is Go's randomised map order and it reshuffles
// whenever the registry cache is invalidated, which model level cooldowns do
// routinely. There is no native sort knob and nowhere in CPA is an order stored,
// so the ordering rule has to live in a plugin.
//
// The plugin hooks the response interceptor, which CPA runs on model list bodies
// after alias substitution. That is the one seam where a plugin sees the final
// cross provider list, and it is the seam upstream points at for list rewrites.
//
// Deploy it with the lowest plugin priority so it runs last in the interceptor
// chain and its order is the one that survives.
package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"encoding/json"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	pluginName    = "model-order"
	pluginAuthor  = "bfSan"
	pluginRepoURL = "https://github.com/bfSan/cpa-plugin-model-order"
)

// version is stamped at build time through -ldflags "-X main.version=...".
var version = "dev"

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

// registrationCapability mirrors the host rpc shape. Only the fields this plugin
// declares are sent; everything else stays false on the host side.
type registrationCapability struct {
	ResponseInterceptor bool `json:"response_interceptor"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if err := configureFromRequest(request); err != nil {
			return nil, err
		}
		return okEnvelope(modelOrderRegistration())

	case pluginabi.MethodResponseInterceptAfter:
		return handleInterceptResponse(request)

	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

// configureFromRequest reads config_yaml from the lifecycle request. A missing or
// unreadable payload falls back to defaults rather than failing the load.
func configureFromRequest(raw []byte) error {
	var configYAML []byte
	if len(raw) > 0 {
		var req struct {
			ConfigYAML []byte `json:"config_yaml"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return loadConfig(nil)
		}
		configYAML = req.ConfigYAML
	}
	return loadConfig(configYAML)
}

func handleInterceptResponse(raw []byte) ([]byte, error) {
	var req pluginapi.ResponseInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		// An unreadable request cannot be judged, so leave the response alone.
		return okEnvelope(pluginapi.ResponseInterceptResponse{})
	}
	if !isModelListing(req) {
		return okEnvelope(pluginapi.ResponseInterceptResponse{})
	}
	ordered, changed := orderBody(req.Body)
	if !changed {
		return okEnvelope(pluginapi.ResponseInterceptResponse{})
	}
	return okEnvelope(pluginapi.ResponseInterceptResponse{Body: ordered})
}

func modelOrderRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginName,
			Version:          version,
			Author:           pluginAuthor,
			GitHubRepository: pluginRepoURL,
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "strategy",
					Type:        pluginapi.ConfigFieldTypeEnum,
					EnumValues:  []string{StrategyGrouped, StrategyName},
					Description: "grouped puts configured buckets first; name sorts the whole list by model ID.",
				},
				{
					Name:        "order",
					Type:        pluginapi.ConfigFieldTypeArray,
					Description: "Ordered glob patterns matched against the model ID, for example [\"auto\", \"gpt-*\"]. Unmatched models tail the list alphabetically.",
				},
				{
					Name:        "case_sensitive",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Description: "Match and compare model IDs case sensitively. Defaults to false.",
				},
			},
		},
		Capabilities: registrationCapability{
			ResponseInterceptor: true,
		},
	}
}

func okEnvelope(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
