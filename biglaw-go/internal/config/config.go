// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package config

import (
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	return v == "true" || v == "1"
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func envFloat(key string, fallback float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return f
}

func envList(key, fallback string) []string {
	v := env(key, fallback)
	parts := strings.Split(v, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// ModelConfig selects the platform's model stack — always an OpenAI-compatible
// endpoint. The default is Qwen (Alibaba) over its DashScope endpoint. No
// Anthropic/Claude path ships in this build by choice. The four tiers map onto
// the historical heavy/mid/light roles plus a vision tier for omnimodal
// document extraction:
//
//	Heavy  — synthesis, debate, the root orchestrator, high-complexity reasoning
//	Mid    — domain managers, T2 specialists, drafting (the default tier)
//	Light  — descriptors, extraction, routing, translation, T3 tool agents
//	Vision — image / scanned-document understanding (Qwen-VL etc.)
//
// PrimaryURL/PrimaryKey point the OpenAI-compatible provider at the stack's host.
type ModelConfig struct {
	Stack      string // "qwen" (default) | "glm" | "kimi" | "custom"
	PrimaryURL string // OpenAI-compatible base URL
	PrimaryKey string // API key for PrimaryURL
	Heavy      string
	Mid        string
	Light      string
	Vision     string
	// ContextTokens, when > 0, declares the context window (in tokens) of the
	// models in play, overriding the heuristic in ContextTokensFor. Set it when
	// the heuristic guesses wrong — e.g. a self-hosted vLLM on the LAN serving a
	// 128K model (heuristic says small), or a cloud endpoint fronting a small
	// model. Env: MODEL_CONTEXT_TOKENS (default 0 = auto).
	ContextTokens int
}

type EmbeddingsConfig struct {
	APIKey     string
	Model      string
	Dimensions int
}

type VectorDBConfig struct {
	DataDir string
}

type APIConfig struct {
	Port   int
	Host   string
	APIKey string
}

type AuthConfig struct {
	Enabled        bool
	SessionSecret  string
	AllowedOrigins []string
	BaseURL        string
	UIURL          string
	AdminEmails    []string
	// OAuth app credentials — register an app with each provider and set the
	// matching env vars (see docs/AUTH_SETUP.md). A provider is offered on the
	// login page only when both its client ID and secret are present.
	GoogleClientID        string
	GoogleClientSecret    string
	MicrosoftClientID     string
	MicrosoftClientSecret string
	LinkedInClientID      string
	LinkedInClientSecret  string
}

type AgentsConfig struct {
	MaxToolIterations int
	// MaxToolResultTokens caps a single tool result (estimated tokens) fed back
	// into the agentic loop. Small/local models run a tiny context window (Ollama
	// defaults qwen to ~4K tokens), so an unbounded retrieval can evict the finding
	// instructions and the model's own output. Bounded reads keep the loop on the
	// tool-calling path without overflowing the window; the cut lands on a word
	// boundary. Note the cap bounds only what re-enters the LOOP context — the
	// staged evidence extractor consumes the full, untruncated result, so a long
	// read_document never loses facts to this cap. -1 (the default) sizes the cap
	// to the model's context window (see ContextTokensFor: 1500 for small/local
	// models, larger for 128K-class models); 0 disables the cap; > 0 is a fixed
	// override. Env: AGENT_MAX_TOOL_RESULT_TOKENS.
	MaxToolResultTokens int
	// MaxEvidencePassages caps how many retrieved passages go into ONE staged
	// evidence-extraction call (passages beyond it roll into further batched
	// calls, they are not dropped). 0 (default) = auto by model context window:
	// 8 on small/local models, more on 128K-class models.
	// Env: AGENT_MAX_EVIDENCE_PASSAGES.
	MaxEvidencePassages int
	// MaxEvidencePerAgent caps the total verbatim evidence quotes one agent locks
	// per round across all extraction batches. 0 (default) = auto by model context
	// window: 8 on small/local models, up to 48 on 128K-class models.
	// Env: AGENT_MAX_EVIDENCE_PER_AGENT.
	MaxEvidencePerAgent int
	// EvidenceQuoteTokens is the per-passage token budget for one verbatim quote
	// (the extractor copies every fact-bearing sentence up to this budget —
	// sentence count is no longer the limit, so a colon-introduced list stays
	// attached to its total). 0 (default) = auto by model context window.
	// Env: AGENT_EVIDENCE_QUOTE_TOKENS.
	EvidenceQuoteTokens int
	// RoundTimeoutMs is the wall-clock cap on a single agent's processing
	// within a round. Prevents one hung provider/tool call from stalling the
	// whole round indefinitely.
	RoundTimeoutMs int
	// GrantRetrievalTools gives every finding-producing agent the document
	// retrieval tools (search_knowledge, read_document, …) when the matter has
	// documents, regardless of its own AllowedTools — so grounding never depends
	// on a heterogeneous agent definition shipping the right tools.
	GrantRetrievalTools bool
	// RequireRetrieval makes an agent call a retrieval tool before its findings
	// are accepted (it is nudged back to the tools if it tries to answer from the
	// document index/titles alone). A capable model that retrieves on its own is
	// unaffected; this backstops weaker local models that otherwise paraphrase.
	// Both knobs keep tool calling as the mechanism while making it reliable.
	RequireRetrieval bool
}

type DyTopoConfig struct {
	SimilarityThreshold float64
	MaxAgentsPerRound   int
	MaxRounds           int
}

type DebateConfig struct {
	CitationRequired        bool
	CitationDropUnsupported bool
	AdversarialEnabled      bool
	VerificationPasses      int
	GateConfidenceThreshold float64
}

// PresentationConfig holds UI/presentation preferences, tunable from the
// admin panel (persisted via the settings store).
type PresentationConfig struct {
	Mode     string // "lawyer" = full legal terminology; "plain" = plain-language framing
	FirmName string
}

// DocuSealConfig — open-source e-signature (https://www.docuseal.com).
type DocuSealConfig struct {
	APIKey  string
	URL     string
	Enabled bool
}

// ClientVoiceConfig governs the Remy/CNTXT client-advocate integration.
// Both knobs are admin-panel tunable (settings store overlay).
type ClientVoiceConfig struct {
	// GateNotes: attach Remy's client-advocacy read to human gates.
	GateNotes bool
	// MatterNotifications: fan client-side messages out to linked
	// Teams/Slack channels. When off, notifications are still stored and
	// audited — they just don't ping anyone.
	MatterNotifications bool
}

type LocalConfig struct {
	OllamaURL           string
	OllamaEnabled       bool
	OllamaModel         string
	OllamaTiers         string
	LocalEmbeddings     bool
	LocalEmbeddingModel string
	LocalInferenceURL   string
	LocalInferenceKey   string
	LocalInferenceModel string
	LocalInferenceTiers string
	InferenceWatts      int
	InferenceRegion     string
	// RequestTimeoutSec bounds a single local HTTP call. The default (300s) suits a model
	// that fits in VRAM; a larger model spilling to CPU (e.g. 14B on an 8GB GPU) can need much
	// longer for a long-form generation, so raise it for those runs.
	RequestTimeoutSec int
}

type PDFConfig struct {
	PythonBin   string
	OutputDir   string
	AllowedDirs []string
}

type PersistenceConfig struct {
	TasksFile       string
	SettingsFile    string
	ProfilesFile    string
	ClientsFile     string
	TimeFile        string
	LearningFile    string
	OcgFile         string
	JobsFile        string
	PreBillsFile    string
	CostFile        string
	PlaybooksFile   string
	ClientVoiceFile string
}

type QueueConfig struct {
	Concurrency    int
	PollIntervalMs int
	MaxRetries     int
	Adapter        string
	RedisURL       string
}

type AgentBillingConfig struct {
	Enabled     bool
	DefaultRate float64
	RateT0      float64
	RateT1      float64
	RateT2      float64
	RateT3      float64
}

type AuditConfig struct {
	Enabled bool
	LogFile string
}

type ConnectorConfig struct {
	APIKey   string
	Endpoint string
	Enabled  bool
}

type GraphEmailConfig struct {
	TenantID     string
	ClientID     string
	ClientSecret string
	UserEmail    string
	AccessToken  string
	Enabled      bool
}

type GmailConfig struct {
	SAKeyJSON   string
	UserEmail   string
	AccessToken string
	Enabled     bool
}

type EmailConfig struct {
	Graph GraphEmailConfig
	Gmail GmailConfig
}

type TeamsBotsConfig struct {
	WebhookSecret      string
	IncomingWebhookURL string
	Enabled            bool
}

type SlackBotConfig struct {
	BotToken       string
	SigningSecret  string
	DefaultChannel string
	Enabled        bool
}

type BotsConfig struct {
	Teams TeamsBotsConfig
	Slack SlackBotConfig
}

type PlaybooksConfig struct {
	File string
}

// LPMConfig governs the Legal Project Management subsystem: the daily
// status-report spine plus the (Phase 2) email intake/routing knobs.
//
//	EmailWriteMode — how much email-writing power the agent has:
//	  "off"       insights only; never composes outbound mail
//	  "channel"   posts drafts into the matter's Teams/Slack channel for comment
//	  "draft"     writes an unsent draft into the mailbox for a human to send
//	  "send_gate" may send, but only after an explicit human approval gate
//
//	IntakeMode — how new email reaches the classifier (Phase 2):
//	  "shared_inbox" scope polling to one project inbox CC'd on everything
//	  "polling"      poll the configured mailbox(es) directly
//	  "both"         shared inbox first, falling back to direct polling
type LPMConfig struct {
	Enabled        bool
	DailyHour      int      // local-time hour (0–23) the daily run fires; 6 == 0600
	Formats        []string // any of: json, docx, markdown
	CorpusFile     string   // append-only JSONL corpus of MatterStatusReports
	ReportDir      string   // rendered DOCX/MD/JSON artifacts land here
	Model          string   // override; empty = low-power routing (Haiku/Ollama/local)
	ChannelPost    bool     // post a short summary + DOCX link to the matter channel
	EmailWriteMode string   // off | channel | draft | send_gate
	IntakeMode     string   // shared_inbox | polling | both
	SharedInbox    string   // mailbox address for shared-inbox intake
	PollIntervalM  int      // email poll interval in minutes
	RoutedFile     string   // append-only JSONL of email→matter routing decisions
	RouteMinConf   float64  // confidence floor below which a routing is "unrouted"
	AllowedDomains []string // recipient-domain allowlist for outbound drafts
	PendingFile    string   // send_gate pending-drafts store

	// Phase 4 historical backfill (grinds old mail on cheap compute).
	BackfillEnabled    bool
	BackfillWindowDays int    // total history to cover
	BackfillStepDays   int    // window size per step
	BackfillMaxPerStep int    // page size per step
	BackfillPauseMs    int    // rate-limit pause between steps
	BackfillCursorFile string // resumable progress cursor
}

// MonitorsConfig governs the firm-wide background monitors.
type MonitorsConfig struct {
	BudgetAlertsEnabled   bool
	BudgetIntervalMin     int
	DocketsEnabled        bool
	DocketsIntervalMin    int
	DocketsFile           string
	RegulatoryIntervalMin int // regulatory auto-enables only when TAVILY_API_KEY is set
}

type ConnectorsConfig struct {
	CourtListener ConnectorConfig
	Ironclad      ConnectorConfig
	IManage       ConnectorConfig
	Definely      ConnectorConfig
	Westlaw       ConnectorConfig
	Everlaw       ConnectorConfig
	Trellis       ConnectorConfig
	Descrybe      ConnectorConfig
	DocuSign      ConnectorConfig
	SolveIntel    ConnectorConfig
	Slack         ConnectorConfig
	GoogleDrive   ConnectorConfig
	Box           ConnectorConfig
	Lawve         ConnectorConfig
	TopCounsel    ConnectorConfig
}

// BlobConfig selects the attachment blob backend (see internal/blob). The
// bundled backends are all open / vendor-neutral: local disk (default), WebDAV
// (RFC 4918), Supabase Storage's native REST API, and an OCI registry via ORAS
// ("the open container thing"). AWS S3 is deliberately not offered.
type BlobConfig struct {
	Backend string // "disk" (default) | "webdav" | "supabase" | "oci"
	Dir     string // disk root

	// WebDAV
	WebDAVURL  string
	WebDAVUser string
	WebDAVPass string

	// Supabase Storage (native REST API, not S3)
	SupabaseURL    string // project URL, e.g. https://<ref>.supabase.co
	SupabaseBucket string
	SupabaseKey    string // service-role or storage key

	// OCI registry (ORAS)
	OCIRef       string // registry/repository, e.g. ghcr.io/acme/biglaw-attachments
	OCIUser      string
	OCIPass      string
	OCIPlainHTTP bool // for local/insecure registries
}

// DatabaseConfig selects the durable persistence backend (see internal/store).
// Default is a local pure-Go SQLite file; set DATABASE_URL to a postgres:// DSN
// for the cloud backend (Supabase/Neon/RDS) with database-level RLS.
type DatabaseConfig struct {
	Backend    string // "sqlite" (default) | "postgres" | "memory"
	SQLitePath string
	URL        string // postgres DSN, when Backend=postgres
}

// ModelsConfig holds user-selectable model assignments for specific roles, plus the list
// of models offered in the UI picker. FigureModel is the small, temp-0 model used for the
// deterministic figure-extraction pass (a cheap 7B-class model is enough and keeps the
// pipeline efficient — the heavy model is not needed for copy-out extraction).
type ModelsConfig struct {
	FigureModel    string   // model id for figure extraction (empty → fall back to the tool/local model)
	SpineModel     string   // model id for the BELO conduct/spine pass (empty → fall back to the bulk model)
	SynthesisModel string   // model id for synthesis/drafting the deliverable (empty → routed default)
	Available      []string // model ids offered in the GUI picker (user-extendable)
}

// DraftingConfig governs the synthesis writing style. When DyTopo is on, each section is
// produced by a bounded writing huddle (lead drafter + contributor agents that critique and
// add grounded specifics over a few rounds), run concurrently across sections, then composed
// by the paged pass. Off → a single drafter per section.
type DraftingConfig struct {
	DyTopo           bool // collaborative DyTopo drafting on/off
	AgentsPerSection int  // huddle size (lead + contributors), default 2
	Rounds           int  // huddle rounds: 1 = draft only; 2-3 = draft→critique→revise
}

type Config struct {
	Models       ModelsConfig
	Drafting     DraftingConfig
	BELOSpine    bool // derive the spine from typed Conduct nodes instead of LLM enumeration
	Model        ModelConfig
	Database     DatabaseConfig
	Embeddings   EmbeddingsConfig
	VectorDB     VectorDBConfig
	API          APIConfig
	Auth         AuthConfig
	Agents       AgentsConfig
	DyTopo       DyTopoConfig
	Debate       DebateConfig
	Presentation PresentationConfig
	DocuSeal     DocuSealConfig
	ClientVoice  ClientVoiceConfig
	Local        LocalConfig
	PDF          PDFConfig
	Persistence  PersistenceConfig
	Queue        QueueConfig
	AgentBilling AgentBillingConfig
	Audit        AuditConfig
	Connectors   ConnectorsConfig
	SearchTavily string
	LogLevel     string
	Blob         BlobConfig
	// ReasoningEffort, when set ("low"/"medium"/"high"), is forwarded as the
	// OpenAI-standard reasoning_effort on heavy "thinking" calls for endpoints
	// that support it (o-series, OpenRouter, DeepSeek-R1, …). Empty = omit it.
	ReasoningEffort string
	// LLMTemperature, when non-nil, overrides the sampling temperature on agent
	// reasoning and synthesis calls. Defaults low (0.2) so the model copies
	// source text verbatim for citations instead of paraphrasing — high
	// temperature is the main cause of unverifiable, reworded quotes. Set
	// LLM_TEMPERATURE to override; LLM_TEMPERATURE=default leaves it to the server.
	LLMTemperature *float64
	Email          EmailConfig
	Bots           BotsConfig
	Playbooks      PlaybooksConfig
	LPM            LPMConfig
	Monitors       MonitorsConfig
	Resilience     ResilienceConfig
}

// ─── Run-hygiene resilience (round-timeout retry + boot task quarantine) ─────
// Self-contained additive block. Consumers: internal/dytopo/engine.go (timeout
// retry + round-starvation surfacing) and internal/orchestrator/restore.go
// (stale-task quarantine at boot).

// ResilienceConfig governs how the runtime degrades when agents blow their
// round budget, and how tasks persisted mid-run are treated at boot.
type ResilienceConfig struct {
	// RoundTimeoutRetryFactor multiplies Agents.RoundTimeoutMs
	// (AGENT_ROUND_TIMEOUT_MS) for the single retry granted to an agent that
	// exceeds the round timeout, before the engine records no findings for it.
	// 2.0 = the retry may take twice the base budget; values < 1 clamp to 1.
	// Env: ROUND_TIMEOUT_RETRY_FACTOR (default 2.0).
	RoundTimeoutRetryFactor float64
	// ResumeRunningTasks restores the pre-quarantine boot behaviour: tasks
	// restored from TASKS_FILE in a mid-run status ("running"/"awaiting_gate")
	// keep that status even though no runner goroutine survives a restart.
	// Default false: such tasks are marked "interrupted" (with a
	// task.interrupted audit event) and must be explicitly resubmitted.
	// Env: RESUME_RUNNING_TASKS (default false).
	ResumeRunningTasks bool
}

// normalizeEnum returns v lowercased if it is in allowed, else fallback.
func normalizeEnum(v, fallback string, allowed ...string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	return fallback
}

func Load() *Config {
	c := &Config{
		Models: ModelsConfig{
			FigureModel:    env("FIGURE_MODEL", ""),     // empty → fall back to the tool/local model
			SpineModel:     env("BELO_SPINE_MODEL", ""), // empty → fall back to the bulk model
			SynthesisModel: env("SYNTHESIS_MODEL", ""),  // empty → routed default (spend 14B on the judged memo)
			Available:      envList("AVAILABLE_MODELS", "qwen2.5:1.5b,qwen2.5:3b,qwen2.5:7b,qwen2.5:14b"),
		},
		Drafting: DraftingConfig{
			DyTopo:           envBool("DYTOPO_DRAFTING", false),
			AgentsPerSection: envInt("DRAFTING_AGENTS_PER_SECTION", 2),
			Rounds:           envInt("DRAFTING_ROUNDS", 2),
		},
		BELOSpine: envBool("BELO_SPINE", false), // spine from typed Conduct nodes vs LLM enumeration
		Database: DatabaseConfig{
			Backend:    normalizeEnum(os.Getenv("DB_BACKEND"), "sqlite", "sqlite", "postgres", "memory"),
			SQLitePath: env("SQLITE_PATH", "./data/biglaw.db"),
			URL:        os.Getenv("DATABASE_URL"),
		},
		Embeddings: EmbeddingsConfig{
			APIKey:     env("OPENAI_API_KEY", ""),
			Model:      env("EMBEDDING_MODEL", "text-embedding-3-small"),
			Dimensions: envInt("EMBEDDING_DIMENSIONS", 1536),
		},
		VectorDB: VectorDBConfig{
			DataDir: env("RUVECTOR_DATA_DIR", "./data"),
		},
		API: APIConfig{
			Port:   envInt("API_PORT", 3101),
			Host:   env("API_HOST", "127.0.0.1"),
			APIKey: env("API_KEY", ""),
		},
		Auth: AuthConfig{
			Enabled:        envBool("AUTH_ENABLED", false),
			SessionSecret:  env("SESSION_SECRET", "dev-insecure-change-me-in-production-please"),
			AllowedOrigins: envList("CORS_ORIGINS", "http://localhost:5173,http://localhost:5174"),
			BaseURL:        env("PUBLIC_BASE_URL", "http://localhost:3101"),
			UIURL:          env("PUBLIC_UI_URL", "http://localhost:5173"),
			AdminEmails:    envList("ADMIN_EMAILS", ""),

			GoogleClientID:        env("GOOGLE_CLIENT_ID", ""),
			GoogleClientSecret:    env("GOOGLE_CLIENT_SECRET", ""),
			MicrosoftClientID:     env("MICROSOFT_CLIENT_ID", ""),
			MicrosoftClientSecret: env("MICROSOFT_CLIENT_SECRET", ""),
			LinkedInClientID:      env("LINKEDIN_CLIENT_ID", ""),
			LinkedInClientSecret:  env("LINKEDIN_CLIENT_SECRET", ""),
		},
		Agents: AgentsConfig{
			MaxToolIterations:   envInt("AGENT_MAX_TOOL_ITERATIONS", 6),
			MaxToolResultTokens: envInt("AGENT_MAX_TOOL_RESULT_TOKENS", -1),
			MaxEvidencePassages: envInt("AGENT_MAX_EVIDENCE_PASSAGES", 0),
			MaxEvidencePerAgent: envInt("AGENT_MAX_EVIDENCE_PER_AGENT", 0),
			EvidenceQuoteTokens: envInt("AGENT_EVIDENCE_QUOTE_TOKENS", 0),
			RoundTimeoutMs:      envInt("AGENT_ROUND_TIMEOUT_MS", 300000),
			GrantRetrievalTools: envBool("AGENT_GRANT_RETRIEVAL_TOOLS", true),
			RequireRetrieval:    envBool("AGENT_REQUIRE_RETRIEVAL", true),
		},
		DyTopo: DyTopoConfig{
			SimilarityThreshold: envFloat("DYTOPO_SIMILARITY_THRESHOLD", 0.68),
			MaxAgentsPerRound:   envInt("DYTOPO_MAX_AGENTS_PER_ROUND", 12),
			MaxRounds:           envInt("DYTOPO_MAX_ROUNDS", 14),
		},
		Debate: DebateConfig{
			CitationRequired:        envBool("DEBATE_CITATION_REQUIRED", true),
			CitationDropUnsupported: envBool("DEBATE_CITATION_DROP_UNSUPPORTED", false),
			AdversarialEnabled:      envBool("DEBATE_ADVERSARIAL_ENABLED", true),
			VerificationPasses:      envInt("DEBATE_VERIFICATION_PASSES", 10),
			GateConfidenceThreshold: envFloat("DEBATE_GATE_CONFIDENCE_THRESHOLD", 0.80),
		},
		Presentation: PresentationConfig{
			Mode:     env("UI_MODE", "lawyer"),
			FirmName: env("FIRM_NAME", ""),
		},
		DocuSeal: DocuSealConfig{
			APIKey: env("DOCUSEAL_API_KEY", ""),
			URL:    env("DOCUSEAL_URL", "http://localhost:3000"),
			// E-signature defaults to on when an API key is present; the
			// admin panel can toggle it and set url/key at runtime.
			Enabled: envBool("DOCUSEAL_ENABLED", os.Getenv("DOCUSEAL_API_KEY") != ""),
		},
		ClientVoice: ClientVoiceConfig{
			GateNotes:           envBool("CLIENT_VOICE_GATE_NOTES", true),
			MatterNotifications: envBool("CLIENT_VOICE_NOTIFICATIONS", true),
		},
		Local: LocalConfig{
			OllamaURL:           env("OLLAMA_URL", "http://localhost:11434"),
			OllamaEnabled:       envBool("OLLAMA_ENABLED", false),
			OllamaModel:         env("OLLAMA_MODEL", "llama3.2"),
			OllamaTiers:         env("OLLAMA_TIERS", "3"),
			LocalEmbeddings:     envBool("LOCAL_EMBEDDINGS", false),
			LocalEmbeddingModel: env("LOCAL_EMBEDDING_MODEL", "nomic-embed-text"),
			LocalInferenceURL:   os.Getenv("LOCAL_INFERENCE_URL"),
			LocalInferenceKey:   env("LOCAL_INFERENCE_KEY", "local"),
			LocalInferenceModel: env("LOCAL_INFERENCE_MODEL", "local-model"),
			LocalInferenceTiers: env("LOCAL_INFERENCE_TIERS", ""),
			InferenceWatts:      envInt("LOCAL_INFERENCE_WATTS", 250),
			InferenceRegion:     env("LOCAL_INFERENCE_REGION", "SEALAND"),
			RequestTimeoutSec:   envInt("LOCAL_REQUEST_TIMEOUT", 300),
		},
		PDF: PDFConfig{
			PythonBin: env("PDF_PYTHON_BIN", "python3"),
			OutputDir: env("PDF_OUTPUT_DIR", "./output/documents"),
		},
		Persistence: PersistenceConfig{
			TasksFile:       env("TASKS_FILE", ".tasks.json"),
			SettingsFile:    env("SETTINGS_FILE", ".settings.json"),
			ProfilesFile:    env("PROFILES_FILE", ".profiles.json"),
			ClientsFile:     env("CLIENTS_FILE", ".clients.json"),
			TimeFile:        env("TIME_FILE", ".time-entries.json"),
			LearningFile:    env("LEARNING_FILE", ".qtable.json"),
			OcgFile:         env("OCG_FILE", ".ocg.json"),
			JobsFile:        env("JOBS_FILE", ".jobs.json"),
			PreBillsFile:    env("PREBILLS_FILE", "./data/prebills.json"),
			CostFile:        env("COST_LOG_FILE", "./data/costs.jsonl"),
			PlaybooksFile:   env("PLAYBOOKS_FILE", "./data/playbooks.json"),
			ClientVoiceFile: env("CLIENT_VOICE_FILE", "./data/clientvoice.json"),
		},
		Queue: QueueConfig{
			Concurrency:    envInt("QUEUE_CONCURRENCY", 3),
			PollIntervalMs: envInt("QUEUE_POLL_INTERVAL_MS", 2000),
			MaxRetries:     envInt("QUEUE_MAX_RETRIES", 3),
			Adapter:        env("QUEUE_ADAPTER", "memory"),
			RedisURL:       env("QUEUE_REDIS_URL", "redis://localhost:6379"),
		},
		AgentBilling: AgentBillingConfig{
			Enabled:     envBool("AGENT_BILLING_ENABLED", true),
			DefaultRate: envFloat("AGENT_BILLING_RATE_DEFAULT", 0),
			RateT0:      envFloat("AGENT_BILLING_RATE_T0", 0),
			RateT1:      envFloat("AGENT_BILLING_RATE_T1", 0),
			RateT2:      envFloat("AGENT_BILLING_RATE_T2", 0),
			RateT3:      envFloat("AGENT_BILLING_RATE_T3", 0),
		},
		Audit: AuditConfig{
			Enabled: envBool("AUDIT_ENABLED", true),
			LogFile: env("AUDIT_LOG_FILE", "./audit.jsonl"),
		},
		SearchTavily: os.Getenv("TAVILY_API_KEY"),
		LogLevel:     env("LOG_LEVEL", "info"),
		Blob: BlobConfig{
			Backend:        normalizeEnum(os.Getenv("BLOB_BACKEND"), "disk", "disk", "webdav", "supabase", "oci"),
			Dir:            env("BLOB_DIR", "./data/attachments"),
			WebDAVURL:      os.Getenv("BLOB_WEBDAV_URL"),
			WebDAVUser:     os.Getenv("BLOB_WEBDAV_USER"),
			WebDAVPass:     os.Getenv("BLOB_WEBDAV_PASS"),
			SupabaseURL:    os.Getenv("BLOB_SUPABASE_URL"),
			SupabaseBucket: env("BLOB_SUPABASE_BUCKET", "attachments"),
			SupabaseKey:    os.Getenv("BLOB_SUPABASE_KEY"),
			OCIRef:         os.Getenv("BLOB_OCI_REF"),
			OCIUser:        os.Getenv("BLOB_OCI_USER"),
			OCIPass:        os.Getenv("BLOB_OCI_PASS"),
			OCIPlainHTTP:   envBool("BLOB_OCI_PLAIN_HTTP", false),
		},
		ReasoningEffort: normalizeEnum(os.Getenv("REASONING_EFFORT"), "", "low", "medium", "high"),
		LLMTemperature:  loadLLMTemperature(),
		Playbooks: PlaybooksConfig{
			File: env("PLAYBOOKS_FILE", "./data/playbooks.json"),
		},
		Email: EmailConfig{
			Graph: GraphEmailConfig{
				TenantID:     os.Getenv("GRAPH_TENANT_ID"),
				ClientID:     os.Getenv("GRAPH_CLIENT_ID"),
				ClientSecret: os.Getenv("GRAPH_CLIENT_SECRET"),
				UserEmail:    os.Getenv("GRAPH_USER_EMAIL"),
				AccessToken:  os.Getenv("GRAPH_ACCESS_TOKEN"),
				Enabled: os.Getenv("GRAPH_ACCESS_TOKEN") != "" ||
					(os.Getenv("GRAPH_TENANT_ID") != "" && os.Getenv("GRAPH_CLIENT_ID") != "" && os.Getenv("GRAPH_CLIENT_SECRET") != ""),
			},
			Gmail: GmailConfig{
				SAKeyJSON:   os.Getenv("GMAIL_SA_KEY_JSON"),
				UserEmail:   os.Getenv("GMAIL_USER_EMAIL"),
				AccessToken: os.Getenv("GMAIL_ACCESS_TOKEN"),
				Enabled:     os.Getenv("GMAIL_ACCESS_TOKEN") != "" || os.Getenv("GMAIL_SA_KEY_JSON") != "",
			},
		},
		Bots: BotsConfig{
			Teams: TeamsBotsConfig{
				WebhookSecret:      os.Getenv("TEAMS_WEBHOOK_SECRET"),
				IncomingWebhookURL: os.Getenv("TEAMS_INCOMING_WEBHOOK_URL"),
				Enabled:            os.Getenv("TEAMS_WEBHOOK_SECRET") != "" || os.Getenv("TEAMS_INCOMING_WEBHOOK_URL") != "",
			},
			Slack: SlackBotConfig{
				BotToken:       os.Getenv("SLACK_BOT_TOKEN"),
				SigningSecret:  os.Getenv("SLACK_SIGNING_SECRET"),
				DefaultChannel: os.Getenv("SLACK_DEFAULT_CHANNEL"),
				Enabled:        os.Getenv("SLACK_BOT_TOKEN") != "" && os.Getenv("SLACK_SIGNING_SECRET") != "",
			},
		},
		Connectors: ConnectorsConfig{
			CourtListener: ConnectorConfig{
				APIKey:   os.Getenv("COURT_LISTENER_API_KEY"),
				Endpoint: env("COURT_LISTENER_API_URL", "https://www.courtlistener.com/api/rest/v4"),
				Enabled:  true,
			},
			Ironclad: ConnectorConfig{
				APIKey:   os.Getenv("IRONCLAD_API_KEY"),
				Endpoint: env("IRONCLAD_MCP_URL", "https://mcp.na1.ironcladapp.com/mcp"),
				Enabled:  os.Getenv("IRONCLAD_API_KEY") != "",
			},
			Westlaw: ConnectorConfig{
				APIKey:   os.Getenv("WESTLAW_API_KEY"),
				Endpoint: env("WESTLAW_MCP_URL", "https://legal-mcp.thomsonreuters.com/mcp"),
				Enabled:  os.Getenv("WESTLAW_API_KEY") != "",
			},
			Everlaw: ConnectorConfig{
				APIKey:   os.Getenv("EVERLAW_API_KEY"),
				Endpoint: env("EVERLAW_MCP_URL", "https://api.everlaw.com/v1/mcp"),
				Enabled:  os.Getenv("EVERLAW_API_KEY") != "",
			},
			Trellis: ConnectorConfig{
				APIKey:   os.Getenv("TRELLIS_API_KEY"),
				Endpoint: env("TRELLIS_MCP_URL", "https://mcp.trellis.law/anthropic"),
				Enabled:  os.Getenv("TRELLIS_API_KEY") != "",
			},
		},
		LPM: LPMConfig{
			Enabled:        envBool("LPM_ENABLED", false),
			DailyHour:      envInt("LPM_DAILY_HOUR", 6),
			Formats:        envList("LPM_REPORT_FORMATS", "json,docx,markdown"),
			CorpusFile:     env("LPM_CORPUS_FILE", "./data/status-reports.jsonl"),
			ReportDir:      env("LPM_REPORT_DIR", "./data/reports"),
			Model:          env("LPM_MODEL", ""),
			ChannelPost:    envBool("LPM_CHANNEL_POST", false),
			EmailWriteMode: normalizeEnum(env("LPM_EMAIL_WRITE_MODE", "off"), "off", "off", "channel", "draft", "send_gate"),
			IntakeMode:     normalizeEnum(env("LPM_INTAKE_MODE", "polling"), "polling", "shared_inbox", "polling", "both"),
			SharedInbox:    env("LPM_SHARED_INBOX", ""),
			PollIntervalM:  envInt("LPM_POLL_INTERVAL_MIN", 15),
			RoutedFile:     env("LPM_ROUTED_FILE", "./data/routed-emails.jsonl"),
			RouteMinConf:   envFloat("LPM_ROUTE_MIN_CONFIDENCE", 0.6),
			AllowedDomains: envList("LPM_ALLOWED_DOMAINS", ""),
			PendingFile:    env("LPM_PENDING_FILE", "./data/pending-drafts.json"),

			BackfillEnabled:    envBool("LPM_BACKFILL_ENABLED", false),
			BackfillWindowDays: envInt("LPM_BACKFILL_WINDOW_DAYS", 365),
			BackfillStepDays:   envInt("LPM_BACKFILL_STEP_DAYS", 7),
			BackfillMaxPerStep: envInt("LPM_BACKFILL_MAX_PER_STEP", 100),
			BackfillPauseMs:    envInt("LPM_BACKFILL_PAUSE_MS", 1000),
			BackfillCursorFile: env("LPM_BACKFILL_CURSOR_FILE", "./data/backfill-cursor.json"),
		},
		Monitors: MonitorsConfig{
			BudgetAlertsEnabled:   envBool("MONITOR_BUDGET_ALERTS", true),
			BudgetIntervalMin:     envInt("MONITOR_BUDGET_INTERVAL_MIN", 60),
			DocketsEnabled:        envBool("MONITOR_DOCKETS", true),
			DocketsIntervalMin:    envInt("MONITOR_DOCKET_INTERVAL_MIN", 30),
			DocketsFile:           env("DOCKETS_FILE", "./data/dockets.json"),
			RegulatoryIntervalMin: envInt("MONITOR_REGULATORY_INTERVAL_MIN", 360),
		},
	}

	// OpenAI chat shortcut: OPENAI_MODEL + OPENAI_API_KEY routes chat through
	// api.openai.com via the existing OpenAI-compatible provider, without the
	// LOCAL_INFERENCE_* incantation. Setting OPENAI_MODEL is the explicit
	// opt-in — OPENAI_API_KEY alone keeps powering only embeddings (many
	// deployments hold an OpenAI key for embeddings and Anthropic for chat).
	// An explicit LOCAL_INFERENCE_URL always wins over the shortcut.
	if c.Local.LocalInferenceURL == "" && os.Getenv("OPENAI_MODEL") != "" && os.Getenv("OPENAI_API_KEY") != "" {
		c.Local.LocalInferenceURL = "https://api.openai.com/v1"
		c.Local.LocalInferenceKey = os.Getenv("OPENAI_API_KEY")
		c.Local.LocalInferenceModel = os.Getenv("OPENAI_MODEL")
		if strings.TrimSpace(c.Local.LocalInferenceTiers) == "" {
			c.Local.LocalInferenceTiers = env("OPENAI_TIERS", "all")
		}
	}

	c.Model = loadModelStack()

	// Run-hygiene resilience knobs — kept in one additive block (see
	// ResilienceConfig for semantics).
	c.Resilience = ResilienceConfig{
		RoundTimeoutRetryFactor: envFloat("ROUND_TIMEOUT_RETRY_FACTOR", 2.0),
		ResumeRunningTasks:      envBool("RESUME_RUNNING_TASKS", false),
	}

	return c
}

// loadLLMTemperature resolves the sampling temperature for reasoning/synthesis
// calls. Default 0.2 (low — favours verbatim copying and determinism for legal
// work); LLM_TEMPERATURE=<float> overrides; LLM_TEMPERATURE=default returns nil
// to leave the server's own default in place.
func loadLLMTemperature() *float64 {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("LLM_TEMPERATURE")))
	if v == "default" || v == "server" {
		return nil
	}
	t := 0.2
	if v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 2 {
			t = f
		}
	}
	return &t
}

// loadModelStack resolves the primary model stack from MODEL_STACK plus optional
// per-tier and endpoint overrides. The default is Qwen over DashScope's
// international OpenAI-compatible endpoint. Generic MODEL_HEAVY/MID/LIGHT/VISION
// overrides win over any preset, and PRIMARY_MODEL_URL/KEY override the endpoint
// — together they make "custom" (or any stack) point anywhere without code.
func loadModelStack() ModelConfig {
	stack := normalizeEnum(os.Getenv("MODEL_STACK"), "qwen", "qwen", "glm", "kimi", "custom")
	m := ModelConfig{Stack: stack}

	switch stack {
	case "glm":
		m.PrimaryURL = env("GLM_BASE_URL", "https://open.bigmodel.cn/api/paas/v4")
		m.PrimaryKey = env("GLM_API_KEY", os.Getenv("PRIMARY_MODEL_KEY"))
		m.Heavy = "glm-4.6"
		m.Mid = "glm-4.5-air"
		m.Light = "glm-4-flash"
		m.Vision = "glm-4v"
	case "kimi":
		m.PrimaryURL = env("KIMI_BASE_URL", env("MOONSHOT_BASE_URL", "https://api.moonshot.ai/v1"))
		m.PrimaryKey = env("KIMI_API_KEY", env("MOONSHOT_API_KEY", os.Getenv("PRIMARY_MODEL_KEY")))
		m.Heavy = "kimi-k2-0905-preview"
		m.Mid = "moonshot-v1-32k"
		m.Light = "moonshot-v1-8k"
		m.Vision = "moonshot-v1-128k-vision-preview"
	case "custom":
		m.PrimaryURL = os.Getenv("PRIMARY_MODEL_URL")
		m.PrimaryKey = os.Getenv("PRIMARY_MODEL_KEY")
		// All tier IDs come from the MODEL_* overrides below.
	default: // qwen
		m.PrimaryURL = env("QWEN_BASE_URL", env("DASHSCOPE_BASE_URL",
			"https://dashscope-intl.aliyuncs.com/compatible-mode/v1"))
		m.PrimaryKey = env("QWEN_API_KEY", env("DASHSCOPE_API_KEY", os.Getenv("PRIMARY_MODEL_KEY")))
		m.Heavy = "qwen-max"
		m.Mid = "qwen-plus"
		m.Light = "qwen-turbo"
		m.Vision = "qwen-vl-max"
	}

	// Endpoint override applies to any non-Claude stack.
	if v := os.Getenv("PRIMARY_MODEL_URL"); v != "" {
		m.PrimaryURL = v
	}
	if v := os.Getenv("PRIMARY_MODEL_KEY"); v != "" {
		m.PrimaryKey = v
	}
	// Per-tier overrides win over the preset (e.g. mix a Claude tier into Qwen).
	m.Heavy = env("MODEL_HEAVY", m.Heavy)
	m.Mid = env("MODEL_MID", m.Mid)
	m.Light = env("MODEL_LIGHT", m.Light)
	m.Vision = env("MODEL_VISION", m.Vision)
	m.ContextTokens = envInt("MODEL_CONTEXT_TOKENS", 0)
	return m
}

// ─── Model context-window heuristic ──────────────────────────────────────────

// Context-window classes for ContextTokensFor. The small class is the local /
// Ollama era the original extraction caps were tuned for (Ollama's default
// num_ctx); the large class is the 128K-class window every current cloud stack
// (Qwen/GLM/Kimi/OpenAI/Claude tiers) ships.
const (
	SmallContextTokens = 8192
	LargeContextTokens = 131072
)

// ContextTokensFor estimates the context window (in tokens) of the model behind
// a routing model ID, so evidence/tool-result caps can scale with what the
// model can actually hold instead of assuming the 4K local floor everywhere.
//
//	MODEL_CONTEXT_TOKENS set        → that value, always (the explicit override)
//	"ollama:…"                      → small (Ollama's default num_ctx is 4–8K)
//	"local:…" at a LAN/loopback URL → small (LM Studio / vLLM on local hardware)
//	"local:…" at a cloud URL        → large (the OPENAI_MODEL shortcut and other
//	                                  hosted endpoints route through "local:")
//	bare stack IDs (qwen-max, …)    → large (every supported stack is 128K-class)
//
// Deliberately conservative: an unrecognisable case degrades to small, which
// only costs recall throughput, never a blown context window.
func (c *Config) ContextTokensFor(model string) int {
	if c.Model.ContextTokens > 0 {
		return c.Model.ContextTokens
	}
	switch {
	case strings.HasPrefix(model, "ollama:"):
		return SmallContextTokens
	case strings.HasPrefix(model, "local:"):
		if isCloudEndpoint(c.Local.LocalInferenceURL) {
			return LargeContextTokens
		}
		return SmallContextTokens
	}
	return LargeContextTokens
}

// isCloudEndpoint reports whether a base URL points at a hosted endpoint rather
// than local/LAN hardware. Loopback, private, link-local, and *.local/*.lan/
// *.internal (incl. host.docker.internal) hosts are local; a resolvable public
// hostname is cloud.
func isCloudEndpoint(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".lan") || strings.HasSuffix(host, ".internal") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified())
	}
	return strings.Contains(host, ".")
}

// Has returns true if the named environment variable is set and non-empty.
// Used by the tools package to detect whether a connector is configured.
func (cc ConnectorsConfig) Has(envKey string) bool {
	return os.Getenv(envKey) != ""
}

// EndpointFor returns the MCP endpoint URL for a named connector tool.
// Returns "" if the connector is not recognised or not configured.
func (cc ConnectorsConfig) EndpointFor(toolName string) string {
	m := map[string]string{
		"court_listener_search":             cc.CourtListener.Endpoint,
		"court_listener_opinion":            cc.CourtListener.Endpoint,
		"court_listener_docket":             cc.CourtListener.Endpoint,
		"westlaw_research":                  cc.Westlaw.Endpoint,
		"westlaw_check_citation":            cc.Westlaw.Endpoint,
		"everlaw_search_documents":          cc.Everlaw.Endpoint,
		"everlaw_get_review_set":            cc.Everlaw.Endpoint,
		"trellis_search_cases":              cc.Trellis.Endpoint,
		"trellis_get_docket":                cc.Trellis.Endpoint,
		"trellis_judge_analytics":           cc.Trellis.Endpoint,
		"descrybe_search_cases":             cc.Descrybe.Endpoint,
		"descrybe_check_citation":           cc.Descrybe.Endpoint,
		"ironclad_search_contracts":         cc.Ironclad.Endpoint,
		"ironclad_get_contract":             cc.Ironclad.Endpoint,
		"docusign_search_contracts":         cc.DocuSign.Endpoint,
		"docusign_get_envelope":             cc.DocuSign.Endpoint,
		"imanage_search":                    cc.IManage.Endpoint,
		"imanage_get_document":              cc.IManage.Endpoint,
		"definely_analyze_structure":        cc.Definely.Endpoint,
		"definely_resolve_definition":       cc.Definely.Endpoint,
		"lawve_review_contract":             cc.Lawve.Endpoint,
		"lawve_search_clauses":              cc.Lawve.Endpoint,
		"google_drive_search":               cc.GoogleDrive.Endpoint,
		"google_drive_get_file":             cc.GoogleDrive.Endpoint,
		"box_search":                        cc.Box.Endpoint,
		"box_get_file":                      cc.Box.Endpoint,
		"slack_search":                      cc.Slack.Endpoint,
		"slack_send_message":                cc.Slack.Endpoint,
		"topcounsel_route_matter":           cc.TopCounsel.Endpoint,
		"topcounsel_get_panel":              cc.TopCounsel.Endpoint,
		"solve_intelligence_search_patents": cc.SolveIntel.Endpoint,
		"solve_intelligence_draft_claims":   cc.SolveIntel.Endpoint,
	}
	return m[toolName]
}
