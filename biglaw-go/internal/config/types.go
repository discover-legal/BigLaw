// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package config

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
	Port      int
	Host      string
	APIKey    string
	ProfileID string
}

const DefaultSessionSecret = "dev-insecure-change-me-in-production-please"

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
	MaxPending     int
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
	Models    ModelsConfig
	Drafting  DraftingConfig
	BELOSpine bool // derive the spine from typed Conduct nodes instead of LLM enumeration
	// ReentrantMachinery re-fires the task-start selective machinery at every DyTopo
	// round boundary, targeted at the round's DELTA: the round's findings are absorbed
	// into the evidence graph, and any NEW entities/claims trigger a targeted specifics
	// re-sweep, a cross-document discrepancy re-join (with the grown alias knowledge),
	// and a defense-lens re-derivation — all deduped against what round 0 already
	// emitted. REENTRANT_MACHINERY=false restores the old one-shot round-0 behavior.
	ReentrantMachinery bool
	Model              ModelConfig
	Database           DatabaseConfig
	Embeddings         EmbeddingsConfig
	VectorDB           VectorDBConfig
	API                APIConfig
	Auth               AuthConfig
	Agents             AgentsConfig
	DyTopo             DyTopoConfig
	Debate             DebateConfig
	Presentation       PresentationConfig
	DocuSeal           DocuSealConfig
	ClientVoice        ClientVoiceConfig
	Local              LocalConfig
	PDF                PDFConfig
	Persistence        PersistenceConfig
	Queue              QueueConfig
	AgentBilling       AgentBillingConfig
	Audit              AuditConfig
	Connectors         ConnectorsConfig
	SearchTavily       string
	LogLevel           string
	Blob               BlobConfig
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
