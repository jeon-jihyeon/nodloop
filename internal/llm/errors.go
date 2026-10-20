package llm

import "errors"

var (
	ErrSchemaRequired = errors.New("llm: schema is required")
	ErrWorkDir        = errors.New("llm: work dir")
	ErrClaudeFailed   = errors.New("llm: claude failed")
	ErrNotResult      = errors.New("llm: claude output is not result json")
	ErrNoOutput       = errors.New("llm: claude result has no structured_output")
	// The llm key of config.json names a backend outside the set
	ErrBackendUnknown = errors.New("llm: unknown backend")
	// A chat backend without an absolute http or https base URL
	ErrBaseURLInvalid = errors.New("llm: base_url is not an absolute http or https URL")
	// A base URL that carries a user or a password where only an env variable may hold the key
	ErrBaseURLCredentials = errors.New("llm: base_url carries credentials. Name the env variable of the key with --key-env")
	// A chat backend without a model
	ErrModelRequired = errors.New("llm: a chat backend needs a model")
	// A request schema the validator cannot resolve
	ErrSchemaInvalid = errors.New("llm: schema does not resolve")
	// A non 2xx answer of a chat endpoint
	ErrStatus = errors.New("llm: endpoint answered an error status")
	// A body that is no chat completion with a choice or that runs past the read limit
	ErrResponseInvalid = errors.New("llm: endpoint answer is not a chat completion")
	// A chat answer that is no JSON or fails the schema twice
	ErrSchemaMismatch = errors.New("llm: answer does not match the schema")
)
