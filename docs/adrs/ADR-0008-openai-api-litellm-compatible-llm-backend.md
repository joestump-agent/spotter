---
status: accepted
date: 2026-02-21
decision-makers: joestump
---

# ADR-0008: OpenAI Chat Completions API with Configurable Base URL over Provider-Specific SDKs for LLM Backend

## Context and Problem Statement

Spotter uses large language models for several AI-powered features: metadata enrichment (artist biographies, album summaries, track analysis, cover art commentary), mixtape generation via the Vibes system, and playlist enhancement. The application needs a way to call LLM APIs that is flexible enough for users to choose their preferred provider — whether that is OpenAI directly, a self-hosted model via Ollama, Azure OpenAI, or a multi-provider proxy like LiteLLM — without requiring code changes or additional dependencies per provider.

## Decision Drivers

* Users run Spotter as a personal, self-hosted application — they should control which LLM provider they use
* Multiple services need LLM access (`internal/enrichers/openai/` for metadata, `internal/vibes/` for mixtape generation and playlist enhancement)
* The OpenAI Chat Completions API has become a de facto standard that most LLM providers and proxies implement
* Adding a separate SDK for each provider (Anthropic, Cohere, Mistral, etc.) would multiply dependencies and integration surface area
* Go does not have a dominant multi-provider LLM framework equivalent to Python's LangChain
* AI features must return structured JSON responses for programmatic parsing

## Considered Options

* **OpenAI Chat Completions API with configurable base URL** — direct HTTP calls to `/chat/completions`, base URL overridable via `SPOTTER_OPENAI_BASE_URL`
* **Provider-specific SDKs** — integrate official SDKs for each LLM provider (OpenAI Go SDK, Anthropic SDK, Cohere SDK)
* **LangChain/LlamaIndex Go equivalents** — use a Go LLM orchestration framework
* **Self-hosted models only** — require users to run local models, removing cloud AI dependency

## Decision Outcome

Chosen option: **OpenAI Chat Completions API with configurable base URL**, because the Chat Completions API is the most widely-supported LLM interface — implemented by OpenAI, Azure OpenAI, LiteLLM, Ollama, vLLM, and dozens of other providers. By making the base URL configurable (`SPOTTER_OPENAI_BASE_URL`, defaulting to `https://api.openai.com/v1`), users can point Spotter at any compatible endpoint without code changes. The implementation uses Go's standard `net/http` client with direct JSON marshaling, adding zero LLM-specific dependencies.

### Consequences

* Good, because zero LLM-specific dependencies — the implementation uses only `net/http`, `encoding/json`, and standard library packages
* Good, because users can switch providers by changing two environment variables (`SPOTTER_OPENAI_BASE_URL` and `SPOTTER_OPENAI_API_KEY`) — no rebuild required
* Good, because LiteLLM proxy support means users can access 100+ models (Anthropic Claude, Google Gemini, Mistral, etc.) through a single endpoint
* Good, because the same API contract (`ChatRequest`/`ChatResponse` structs) is shared by both the enricher and vibes subsystems, providing consistency
* Bad, because provider-specific features (Anthropic's extended thinking, OpenAI's function calling beyond `response_format`) are not accessible
* Bad, because non-OpenAI-compatible providers (e.g., Cohere's native API) cannot be used directly — they require a proxy like LiteLLM

### Confirmation

Compliance is confirmed by checking that `internal/enrichers/openai/openai.go` and `internal/vibes/generator.go` both construct HTTP requests to `baseURL + "/chat/completions"` using the `ChatRequest`/`ChatResponse` structs. The base URL is read from `config.OpenAI.BaseURL` with a fallback to `"https://api.openai.com/v1"`. No OpenAI SDK import (e.g., `github.com/openai/openai-go`) should appear in `go.mod`.

## Pros and Cons of the Options

### OpenAI Chat Completions API with Configurable Base URL

Direct HTTP POST to `{baseURL}/chat/completions` with JSON request/response bodies. The `callOpenAI()` method in both `internal/enrichers/openai/openai.go:301` and `internal/vibes/generator.go:591` reads `config.OpenAI.BaseURL`, defaults to `"https://api.openai.com/v1"`, strips trailing slashes, and constructs the full endpoint URL. The model defaults to `gpt-4o` (constant `defaultModel` in the enricher, `GetVibesModel()` fallback in vibes). Authentication uses `Bearer` token via the `Authorization` header. Responses are parsed into typed Go structs (`ChatResponse`) with error handling for API errors, empty choices, and non-200 status codes. JSON response format is requested via `ResponseFormat{Type: "json_object"}`.

* Good, because compatible with OpenAI, Azure OpenAI, LiteLLM, Ollama, vLLM, LocalAI, and any provider implementing the Chat Completions API
* Good, because the `ChatRequest` and `ChatResponse` structs in both subsystems are self-contained — no external SDK to version or update
* Good, because configurable per-subsystem: vibes can override the model via `SPOTTER_VIBES_MODEL` while enrichment uses `SPOTTER_OPENAI_MODEL`
* Good, because timeout is configurable — enricher uses 120s default, vibes uses `SPOTTER_VIBES_TIMEOUT_SECONDS`
* Neutral, because `ChatRequest`/`ChatResponse` structs are duplicated between `internal/enrichers/openai/` and `internal/vibes/` rather than shared — acceptable given the different usage patterns (enricher sends images, vibes does not)
* Bad, because no streaming support — responses are read in full via `io.ReadAll`, which means long generations block until complete
* Bad, because no automatic retries or exponential backoff — a transient API failure will surface immediately as an error

### Provider-Specific SDKs

Import and use the official SDK for each LLM provider: `github.com/openai/openai-go` for OpenAI, `github.com/anthropics/anthropic-sdk-go` for Anthropic, etc.

* Good, because SDKs provide typed, idiomatic Go interfaces with automatic retries and streaming built in
* Good, because provider-specific features (function calling, tool use, vision APIs) are fully accessible
* Bad, because each new provider requires a new SDK dependency, integration code, and configuration surface
* Bad, because SDKs have their own release cadences and breaking changes — multiple dependencies to track
* Bad, because the application would need a provider abstraction layer to switch between SDKs, duplicating what LiteLLM already provides

### LangChain/LlamaIndex Go Equivalents

Use a Go LLM orchestration framework such as `tmc/langchaingo` to abstract provider differences.

* Good, because a single abstraction layer supports multiple providers
* Good, because includes utilities for prompt templating, chain-of-thought, and RAG
* Bad, because Go LLM frameworks are significantly less mature than their Python counterparts
* Bad, because heavy transitive dependency tree for features Spotter does not need (vector stores, agents, RAG pipelines)
* Bad, because Spotter already implements its own prompt templating via Go's `text/template` — a framework would duplicate this

### Self-Hosted Models Only

Remove cloud AI support and require users to run local models (Ollama, llama.cpp).

* Good, because no API keys needed — reduces configuration and cost
* Good, because all data stays local — no privacy concerns about sending music metadata to cloud APIs
* Bad, because local model quality is significantly lower than cloud models for the nuanced music analysis Spotter performs
* Bad, because requires users to have hardware capable of running LLMs — contradicts the lightweight personal server design
* Bad, because eliminates the most common deployment path (cloud API with an API key)

## More Information

* Enricher OpenAI integration: `internal/enrichers/openai/openai.go` — `callOpenAI()`, `ChatRequest`/`ChatResponse` types, base URL fallback logic
* Vibes mixtape generation: `internal/vibes/generator.go` — `callOpenAI()`, model selection via `GetVibesModel()`, configurable temperature and max tokens
* Configuration: `internal/config/config.go:85-89` — `OpenAI` struct with `APIKey`, `BaseURL`, `Model` fields; defaults at lines 229-231
* Environment variables: `SPOTTER_OPENAI_API_KEY`, `SPOTTER_OPENAI_BASE_URL`, `SPOTTER_OPENAI_MODEL`, `SPOTTER_VIBES_MODEL`
* Example configuration: `.env.example:19-22` — documents the three OpenAI environment variables
* Default model constant: `internal/enrichers/openai/openai.go:41` — `defaultModel = "gpt-4o"`
* Prompt templates: `data/prompts/` — Go `text/template` files for artist, album, track, and mixtape generation prompts
