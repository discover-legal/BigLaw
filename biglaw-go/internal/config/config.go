// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package config

import (
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

type AnthropicConfig struct {
	APIKey               string
	Model                string
	BaseURL              string
	ThinkingBudgetTokens int
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
}

type AgentsConfig struct {
	MaxToolIterations int
}

type DyTopoConfig struct {
	SimilarityThreshold float64
	MaxAgentsPerRound   int
	MaxRounds           int
}

type DebateConfig struct {
	CitationRequired        bool
	AdversarialEnabled      bool
	VerificationPasses      int
	GateConfidenceThreshold float64
}

type LocalConfig struct {
	OllamaURL             string
	OllamaEnabled         bool
	OllamaModel           string
	OllamaTiers           string
	LocalEmbeddings       bool
	LocalEmbeddingModel   string
	LocalInferenceURL     string
	LocalInferenceKey     string
	LocalInferenceModel   string
	LocalInferenceTiers   string
	InferenceWatts        int
	InferenceRegion       string
}

type PDFConfig struct {
	PythonBin   string
	OutputDir   string
	AllowedDirs []string
}

type PersistenceConfig struct {
	TasksFile    string
	SettingsFile string
	ProfilesFile string
	ClientsFile  string
	TimeFile     string
	LearningFile string
	OcgFile      string
	JobsFile     string
	PreBillsFile  string
	CostFile      string
	PlaybooksFile string
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
	Graph  GraphEmailConfig
	Gmail  GmailConfig
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
	Teams  TeamsBotsConfig
	Slack  SlackBotConfig
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
}

type ConnectorsConfig struct {
	CourtListener  ConnectorConfig
	Ironclad       ConnectorConfig
	IManage        ConnectorConfig
	Definely       ConnectorConfig
	Westlaw        ConnectorConfig
	Everlaw        ConnectorConfig
	Trellis        ConnectorConfig
	Descrybe       ConnectorConfig
	DocuSign       ConnectorConfig
	SolveIntel     ConnectorConfig
	Slack          ConnectorConfig
	GoogleDrive    ConnectorConfig
	Box            ConnectorConfig
	Lawve          ConnectorConfig
	TopCounsel     ConnectorConfig
}

type Config struct {
	Anthropic     AnthropicConfig
	Embeddings    EmbeddingsConfig
	VectorDB      VectorDBConfig
	API           APIConfig
	Auth          AuthConfig
	Agents        AgentsConfig
	DyTopo        DyTopoConfig
	Debate        DebateConfig
	Local         LocalConfig
	PDF           PDFConfig
	Persistence   PersistenceConfig
	Queue         QueueConfig
	AgentBilling  AgentBillingConfig
	Audit         AuditConfig
	Connectors    ConnectorsConfig
	SearchTavily  string
	LogLevel      string
	Email         EmailConfig
	Bots          BotsConfig
	Playbooks     PlaybooksConfig
	LPM           LPMConfig
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
		Anthropic: AnthropicConfig{
			APIKey:               os.Getenv("ANTHROPIC_API_KEY"),
			Model:                env("ANTHROPIC_MODEL", "claude-opus-4-8"),
			BaseURL:              os.Getenv("ANTHROPIC_BASE_URL"),
			ThinkingBudgetTokens: envInt("THINKING_BUDGET_TOKENS", 10000),
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
		},
		Agents: AgentsConfig{
			MaxToolIterations: envInt("AGENT_MAX_TOOL_ITERATIONS", 6),
		},
		DyTopo: DyTopoConfig{
			SimilarityThreshold: envFloat("DYTOPO_SIMILARITY_THRESHOLD", 0.68),
			MaxAgentsPerRound:   envInt("DYTOPO_MAX_AGENTS_PER_ROUND", 12),
			MaxRounds:           envInt("DYTOPO_MAX_ROUNDS", 14),
		},
		Debate: DebateConfig{
			CitationRequired:        envBool("DEBATE_CITATION_REQUIRED", true),
			AdversarialEnabled:      envBool("DEBATE_ADVERSARIAL_ENABLED", true),
			VerificationPasses:      envInt("DEBATE_VERIFICATION_PASSES", 10),
			GateConfidenceThreshold: envFloat("DEBATE_GATE_CONFIDENCE_THRESHOLD", 0.80),
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
		},
		PDF: PDFConfig{
			PythonBin: env("PDF_PYTHON_BIN", "python3"),
			OutputDir: env("PDF_OUTPUT_DIR", "./output/documents"),
		},
		Persistence: PersistenceConfig{
			TasksFile:    env("TASKS_FILE", ".tasks.json"),
			SettingsFile: env("SETTINGS_FILE", ".settings.json"),
			ProfilesFile: env("PROFILES_FILE", ".profiles.json"),
			ClientsFile:  env("CLIENTS_FILE", ".clients.json"),
			TimeFile:     env("TIME_FILE", ".time-entries.json"),
			LearningFile: env("LEARNING_FILE", ".qtable.json"),
			OcgFile:      env("OCG_FILE", ".ocg.json"),
			JobsFile:     env("JOBS_FILE", ".jobs.json"),
			PreBillsFile:  env("PREBILLS_FILE", "./data/prebills.json"),
			CostFile:      env("COST_LOG_FILE", "./data/costs.jsonl"),
			PlaybooksFile: env("PLAYBOOKS_FILE", "./data/playbooks.json"),
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
				Enabled: os.Getenv("TEAMS_WEBHOOK_SECRET") != "" || os.Getenv("TEAMS_INCOMING_WEBHOOK_URL") != "",
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
		},
	}
	return c
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
		"court_listener_search":  cc.CourtListener.Endpoint,
		"court_listener_opinion": cc.CourtListener.Endpoint,
		"court_listener_docket":  cc.CourtListener.Endpoint,
		"westlaw_research":        cc.Westlaw.Endpoint,
		"westlaw_check_citation":  cc.Westlaw.Endpoint,
		"everlaw_search_documents": cc.Everlaw.Endpoint,
		"everlaw_get_review_set":   cc.Everlaw.Endpoint,
		"trellis_search_cases":     cc.Trellis.Endpoint,
		"trellis_get_docket":       cc.Trellis.Endpoint,
		"trellis_judge_analytics":  cc.Trellis.Endpoint,
		"descrybe_search_cases":    cc.Descrybe.Endpoint,
		"descrybe_check_citation":  cc.Descrybe.Endpoint,
		"ironclad_search_contracts": cc.Ironclad.Endpoint,
		"ironclad_get_contract":     cc.Ironclad.Endpoint,
		"docusign_search_contracts": cc.DocuSign.Endpoint,
		"docusign_get_envelope":     cc.DocuSign.Endpoint,
		"imanage_search":      cc.IManage.Endpoint,
		"imanage_get_document": cc.IManage.Endpoint,
		"definely_analyze_structure":  cc.Definely.Endpoint,
		"definely_resolve_definition": cc.Definely.Endpoint,
		"lawve_review_contract": cc.Lawve.Endpoint,
		"lawve_search_clauses":  cc.Lawve.Endpoint,
		"google_drive_search":   cc.GoogleDrive.Endpoint,
		"google_drive_get_file": cc.GoogleDrive.Endpoint,
		"box_search":   cc.Box.Endpoint,
		"box_get_file": cc.Box.Endpoint,
		"slack_search":       cc.Slack.Endpoint,
		"slack_send_message": cc.Slack.Endpoint,
		"topcounsel_route_matter": cc.TopCounsel.Endpoint,
		"topcounsel_get_panel":    cc.TopCounsel.Endpoint,
		"solve_intelligence_search_patents": cc.SolveIntel.Endpoint,
		"solve_intelligence_draft_claims":   cc.SolveIntel.Endpoint,
	}
	return m[toolName]
}
