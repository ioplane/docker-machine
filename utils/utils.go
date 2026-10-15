// Package utils provides pointer conversions and sanitized diagnostic helpers.
package utils

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

// PrintToJSON validates JSON without logging payloads or caller-provided messages.
func PrintToJSON(value any, _ string) {
	pretty, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		log.Print("[DEBUG] JSON serialization failed; payload omitted")

		return
	}

	log.Printf("[DEBUG] JSON payload omitted; encoded_bytes=%d", len(pretty))
}

// ToJSONString returns indented JSON or JSON null if serialization fails.
func ToJSONString(value any) string {
	pretty, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "null"
	}

	return string(pretty)
}

// DebugRequest logs metadata without credentials, URLs, headers, or request bodies.
func DebugRequest(request *http.Request) {
	if request == nil {
		return
	}

	log.Print("[DEBUG] HTTP request details omitted")
}

// DebugResponse logs metadata without response headers or payloads.
func DebugResponse(response *http.Response) {
	if response == nil {
		return
	}

	log.Printf("[DEBUG] HTTP response status=%d headers=%d body_present=%t",
		response.StatusCode, len(response.Header), response.Body != nil)
}

// ConvertMapString converts map values to their string representations.
func ConvertMapString(original map[string]any) map[string]string {
	converted := make(map[string]string, len(original))
	for key, value := range original {
		converted[key] = fmt.Sprint(value)
	}

	return converted
}
