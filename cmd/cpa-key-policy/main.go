//go:build cshared

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

static int call_host_auth_list(const cliproxy_host_api* host, cliproxy_buffer* response) {
	if (host == NULL || host->call == NULL || host->free_buffer == NULL) {
		return 1;
	}
	static const char method[] = "host.auth.list";
	static const uint8_t request[] = "{}";
	return host->call(host->host_ctx, method, request, sizeof(request) - 1, response);
}

static void free_host_buffer(const cliproxy_host_api* host, void* ptr, size_t len) {
	if (host != NULL && host->free_buffer != NULL && ptr != NULL) {
		host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"errors"
	"sync"
	"unsafe"

	"cpa-key-policy/internal/plugin"
)

var app = plugin.NewApp()

var hostAPIMu sync.RWMutex
var hostAPI C.cliproxy_host_api

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, api *C.cliproxy_plugin_api) C.int {
	hostAPIMu.Lock()
	if host == nil {
		hostAPI = C.cliproxy_host_api{}
	} else {
		hostAPI = *host
	}
	hostAPIMu.Unlock()
	app.SetAuthFileLister(listHostAuthFiles)
	if api == nil {
		return 1
	}
	api.abi_version = C.uint32_t(plugin.ABIVersion)
	api.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	api.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	api.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, plugin.ErrorEnvelope("invalid_method", "method is required", 400))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, err := app.HandleMethod(C.GoString(method), requestBytes)
	if err != nil {
		writeResponse(response, plugin.ErrorEnvelope("plugin_error", err.Error(), 500))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	app.Shutdown()
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

func listHostAuthFiles() ([]plugin.HostAuthFile, error) {
	hostAPIMu.RLock()
	host := hostAPI
	hostAPIMu.RUnlock()
	var response C.cliproxy_buffer
	code := C.call_host_auth_list(&host, &response)
	if response.ptr != nil {
		defer C.free_host_buffer(&host, response.ptr, response.len)
	}
	if code != 0 {
		return nil, errors.New("host.auth.list callback failed or is unsupported")
	}
	if response.ptr == nil || response.len == 0 || uint64(response.len) > uint64(1<<31-1) {
		return nil, errors.New("host.auth.list returned an invalid buffer")
	}
	// Go owns this copy; the deferred trampoline releases the original through
	// the host allocator, never through the plugin's C.free.
	raw := C.GoBytes(response.ptr, C.int(response.len))
	var envelope plugin.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil || !envelope.OK {
		return nil, errors.New("host.auth.list returned an invalid or unsuccessful envelope")
	}
	var result struct {
		Files *[]plugin.HostAuthFile `json:"files"`
	}
	if err := json.Unmarshal(envelope.Result, &result); err != nil || result.Files == nil {
		return nil, errors.New("host.auth.list returned an invalid directory")
	}
	return *result.Files, nil
}
