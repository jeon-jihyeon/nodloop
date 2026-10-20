package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
)

// The OpenAPI version of the document
const openAPIVersion = "3.1.0"

// Kinds of the published index
type kinds []schema.Kind

// The kinds the server keeps records of
// The server runs no guard so it keeps no guard log
func (ks kinds) served() kinds {
	var out kinds
	for _, k := range ks {
		if k.Store != schema.StoreGuard {
			out = append(out, k)
		}
	}
	return out
}

// Answers the OpenAPI document of the routes of the server
// It names no tenant and no record so a client reads it without a key
type openAPIHandler struct {
	files   schema.Files
	version string
	// Where a failure is written since its answer names none of it
	log io.Writer
}

func (h openAPIHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	doc, err := openAPIDocument(h.files, h.version)
	if err != nil {
		fmt.Fprintf(h.log, "nodloop-server: openapi: %v\n", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(doc)
}

// The OpenAPI 3.1 document of every HTTP route of the server built from the published schemas
// 1. each kind the server serves is a component holding the published file of its newest version as it is
// 2. a page of GET /v1/records/{kind} holds records of those components told apart by record_kind
// so a client and the records share one contract
func openAPIDocument(files schema.Files, version string) (json.RawMessage, error) {
	ix, err := files.Index()
	if err != nil {
		return nil, err
	}
	components := map[string]any{}
	var names, refs []any
	mapping := map[string]string{}
	for _, k := range kinds(ix.Kinds).served() {
		file, err := k.File(k.Version)
		if err != nil {
			return nil, err
		}
		ref := "#/components/schemas/" + k.Name
		components[k.Name] = json.RawMessage(file)
		names, refs, mapping[k.Name] = append(names, k.Name), append(refs, map[string]any{"$ref": ref}), ref
	}
	components["page"] = map[string]any{
		"type": "object", "required": []string{"records", "next"},
		"properties": map[string]any{
			"records": map[string]any{
				"type":  "array",
				"items": map[string]any{"oneOf": refs, "discriminator": map[string]any{"propertyName": "record_kind", "mapping": mapping}},
			},
			"next": map[string]any{"type": "string", "description": "the cursor to pass as since next, the same while nothing newer is settled"},
		},
	}
	reports := make([]any, 0, len(loop.ReportNames()))
	for _, n := range loop.ReportNames() {
		reports = append(reports, string(n))
	}
	doc := map[string]any{
		"openapi": openAPIVersion,
		"info": map[string]any{
			"title": "nodloop-server", "version": version,
			"description": "The raw records of nodloop in their published schemas and the MCP tools of a key's role",
		},
		"security": []any{map[string]any{"key": []any{}}},
		"paths":    paths(names, reports),
		"components": map[string]any{
			"schemas":         components,
			"securitySchemes": map[string]any{"key": map[string]any{"type": "http", "scheme": "bearer", "description": "a key of nodloop-server key add"}},
		},
	}
	return json.Marshal(doc)
}

// The routes of the handler with their parameters and answers
func paths(kindNames, reports []any) map[string]any {
	text := func(description string) map[string]any {
		return map[string]any{"description": description, "content": map[string]any{"text/plain": map[string]any{"schema": map[string]any{"type": "string"}}}}
	}
	jsonOf := func(description string, s any) map[string]any {
		return map[string]any{"description": description, "content": map[string]any{"application/json": map[string]any{"schema": s}}}
	}
	// The answers of a route behind a key with the answers of its own
	answers := func(ok map[string]any, more map[string]any) map[string]any {
		out := map[string]any{
			"200": ok, "401": text("no key or an unknown key"), "403": text("a role that may not read it"), "404": text("a name the route does not know"),
			"500": text("an internal failure"),
		}
		for code, answer := range more {
			out[code] = answer
		}
		return out
	}
	query := func(name, description string, s map[string]any) map[string]any {
		return map[string]any{"name": name, "in": "query", "description": description, "schema": s}
	}
	return map[string]any{
		"/healthz": map[string]any{"get": map[string]any{
			"summary": "Whether the server runs", "security": []any{}, "responses": map[string]any{"200": text("ok")},
		}},
		"/v1/openapi.json": map[string]any{"get": map[string]any{
			"summary": "This document", "security": []any{}, "responses": map[string]any{"200": jsonOf("the OpenAPI document", map[string]any{"type": "object"})},
		}},
		"/v1/records/{kind}": map[string]any{"get": map[string]any{
			"summary":     "One page of the raw records of a kind of the tenant of the key",
			"description": "Records by time then record_id after the cursor since, only those at least 10 s old, with the free text of the kind only with content true",
			"parameters": []any{
				map[string]any{"name": "kind", "in": "path", "required": true, "schema": map[string]any{"type": "string", "enum": kindNames}},
				query("since", "the next cursor of the page before, empty for the first page", map[string]any{"type": "string"}),
				query("limit", "records per page, cut to 1000", map[string]any{"type": "integer", "default": schema.DefaultLimit, "maximum": schema.MaxLimit}),
				query("content", "keep the free text of the kind", map[string]any{"type": "boolean", "default": false}),
			},
			"responses": answers(jsonOf("the page", map[string]any{"$ref": "#/components/schemas/page"}),
				map[string]any{"400": text("a limit, a content flag or a cursor the page cannot read")}),
		}},
		"/v1/reports/{name}": map[string]any{"get": map[string]any{
			"summary": "One report of the tenant of the key as JSON", "deprecated": true,
			"description": "Removed in the next release since the record pages carry the contract",
			"parameters":  []any{map[string]any{"name": "name", "in": "path", "required": true, "schema": map[string]any{"type": "string", "enum": reports}}},
			"responses":   answers(jsonOf("the report, whose shape no published schema covers", map[string]any{}), nil),
		}},
		"/mcp": map[string]any{"post": map[string]any{
			"summary":     "The MCP tools of the role of the key over stateless streamable HTTP",
			"requestBody": map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object"}}}},
			"responses": map[string]any{
				"200": map[string]any{"description": "the JSON-RPC answer", "content": map[string]any{
					"application/json": map[string]any{"schema": map[string]any{"type": "object"}}, "text/event-stream": map[string]any{"schema": map[string]any{"type": "string"}},
				}},
				"401": text("no key or an unknown key"),
			},
		}},
	}
}
