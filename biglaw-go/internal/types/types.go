// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package types

import "time"

// ─── Agent taxonomy ───────────────────────────────────────────────────────────

type AgentTier int

const (
	TierRoot       AgentTier = 0
	TierManager    AgentTier = 1
	TierSpecialist AgentTier = 2
	TierTool       AgentTier = 3
)

type AgentType string

const (
	AgentTypeRoot       AgentType = "root"
	AgentTypeManager    AgentType = "manager"
	AgentTypeSpecialist AgentType = "specialist"
	AgentTypeTool       AgentType = "tool"
)

type AgentDomain string

const (
	DomainOrchestration AgentDomain = "orchestration"
	DomainResearch      AgentDomain = "research"
	DomainInvestigation AgentDomain = "investigation"
	DomainDrafting      AgentDomain = "drafting"
	DomainReview        AgentDomain = "review"
	DomainCompliance    AgentDomain = "compliance"
	DomainAnalysis      AgentDomain = "analysis"
	DomainTool          AgentDomain = "tool"
)

type AgentDefinition struct {
	ID            string                 `json:"id"`
	Name          string                 `json:"name"`
	Tier          AgentTier              `json:"tier"`
	Type          AgentType              `json:"type"`
	Domain        AgentDomain            `json:"domain"`
	Description   string                 `json:"description"`
	SystemPrompt  string                 `json:"systemPrompt"`
	AllowedTools  []string               `json:"allowedTools"`
	Skills        []string               `json:"skills"`
	Jurisdictions []string               `json:"jurisdictions,omitempty"`
	BillingRate   *float64               `json:"billingRate,omitempty"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
	// Runtime field for Q-learning performance score
	SuccessScore float64   `json:"successScore,omitempty"`
	Embedding    []float32 `json:"-"`
}

// ─── DyTopo core ──────────────────────────────────────────────────────────────

type TaskPhase string

const (
	PhaseIntake         TaskPhase = "intake"
	PhaseResearch       TaskPhase = "research"
	PhaseAnalysis       TaskPhase = "analysis"
	PhaseReconciliation TaskPhase = "reconciliation"
	PhaseDrafting       TaskPhase = "drafting"
	PhaseReview         TaskPhase = "review"
	PhaseVerification   TaskPhase = "verification"
	PhaseDelivery       TaskPhase = "delivery"
)

// ─── Evidence graph (controversies) ─────────────────────────────────────────────
//
// Claim and Controversy are deliberately graph-shaped so the future TypeDB migration
// is a projection, not a redesign. Mapping (per the evidence-graph / conflicts seam):
//   Claim       → a polymorphic `claim` entity (sub-typed by Kind: monetary/temporal/
//                 count/categorical), linked `about` a subject and `asserted-by` a source.
//   Controversy → a `contradiction` relation relating ≥2 conflicting claims on one
//                 subject — exactly the typed-inference target TypeDB will own.
// Until then the same shapes are detected in-process by the reconciliation analyst.

// Claim is a single asserted value about a subject, attributed to a source document.
type Claim struct {
	Subject string `json:"subject"`        // what is asserted about (e.g. "omnibus equity trade count")
	Value   string `json:"value"`          // the asserted value (e.g. "4,217")
	Source  string `json:"source"`         // the document the assertion comes from
	Kind    string `json:"kind,omitempty"` // monetary | temporal | count | categorical
}

// Controversy is a cross-document conflict: ≥2 claims about the same subject whose
// values disagree. The reconciliation round recruits a specialist to analyse each.
type Controversy struct {
	Subject      string  `json:"subject"`
	Kind         string  `json:"kind,omitempty"`
	Claims       []Claim `json:"claims"`
	Significance string  `json:"significance,omitempty"` // why the discrepancy matters
}

type RoundGoal struct {
	ID              string    `json:"id"`
	Round           int       `json:"round"`
	Phase           TaskPhase `json:"phase"`
	Description     string    `json:"description"`
	ExpectedOutputs []string  `json:"expectedOutputs"`
}

type NeedDescriptor struct {
	AgentID   string    `json:"agentId"`
	Text      string    `json:"text"`
	Embedding []float32 `json:"-"`
}

type OfferDescriptor struct {
	AgentID   string    `json:"agentId"`
	Text      string    `json:"text"`
	Embedding []float32 `json:"-"`
}

type CommunicationEdge struct {
	From       string  `json:"from"`
	To         string  `json:"to"`
	Similarity float64 `json:"similarity"`
	OfferText  string  `json:"offerText"`
}

type AgentMessage struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Content   string    `json:"content"`
	Round     int       `json:"round"`
	Timestamp time.Time `json:"timestamp"`
}

type RoundState struct {
	RoundID        string              `json:"roundId"`
	Goal           RoundGoal           `json:"goal"`
	ActiveAgentIDs []string            `json:"activeAgentIds"`
	Edges          []CommunicationEdge `json:"edges"`
	Messages       []AgentMessage      `json:"messages"`
	Findings       []Finding           `json:"findings"`
	Status         string              `json:"status"`
	StartedAt      time.Time           `json:"startedAt"`
	CompletedAt    *time.Time          `json:"completedAt,omitempty"`
	// Starved is true when the round ended with zero findings from every
	// active agent (each attempt — including the one extended-budget retry —
	// timed out or errored). A starved round means the pipeline ran degraded;
	// see the round.starved audit event and Task.StarvedRounds.
	Starved bool `json:"starved,omitempty"`
}

// StarvedRound identifies a round that completed with zero findings from all
// of its agents — the task-level annotation counterpart of RoundState.Starved.
type StarvedRound struct {
	Round int       `json:"round"`
	Phase TaskPhase `json:"phase"`
}

// ─── Memory ──────────────────────────────────────────────────────────────────

type IntraRoundMemory struct {
	RoundID          string                    `json:"roundId"`
	ReceivedMessages map[string][]AgentMessage `json:"receivedMessages"`
	AgentFindings    map[string][]Finding      `json:"agentFindings"`
	SharedContext    []string                  `json:"sharedContext"`
}

type MemoryEntry struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"taskId"`
	Round     int       `json:"round"`
	Phase     TaskPhase `json:"phase"`
	AgentID   string    `json:"agentId,omitempty"`
	Content   string    `json:"content"`
	Embedding []float32 `json:"-"`
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"createdAt"`
}

// ─── Protocol types ───────────────────────────────────────────────────────────

type Citation struct {
	Source               string `json:"source"`
	Quote                string `json:"quote"`
	Page                 *int   `json:"page,omitempty"`
	MechanicallyVerified bool   `json:"mechanicallyVerified"`
}

type Challenge struct {
	ChallengerID   string     `json:"challengerId"`
	ChallengerName string     `json:"challengerName"`
	Content        string     `json:"content"`
	Citations      []Citation `json:"citations"`
	Resolution     string     `json:"resolution,omitempty"`
	ResolvedAt     *time.Time `json:"resolvedAt,omitempty"`
}

type VerificationCheck struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Notes  string `json:"notes,omitempty"`
}

type VerificationResult struct {
	FindingID   string              `json:"findingId"`
	Checks      []VerificationCheck `json:"checks"`
	Passed      bool                `json:"passed"`
	CompletedAt time.Time           `json:"completedAt"`
}

type Finding struct {
	ID                 string              `json:"id"`
	AgentID            string              `json:"agentId"`
	AgentName          string              `json:"agentName"`
	Content            string              `json:"content"`
	Citations          []Citation          `json:"citations"`
	Confidence         float64             `json:"confidence"`
	Challenged         bool                `json:"challenged"`
	Challenge          *Challenge          `json:"challenge,omitempty"`
	Resolved           bool                `json:"resolved"`
	VerificationResult *VerificationResult `json:"verificationResult,omitempty"`
	Round              int                 `json:"round"`
	Timestamp          time.Time           `json:"timestamp"`

	// A finding is a CONCLUSION (the agent's analysis, in Content — never
	// expected to match source wording) plus zero or more EVIDENCE items (its
	// Citations — each a verbatim quote that can be mechanically verified
	// against the cited source). The two are judged differently: a conclusion
	// is the model's reasoning; evidence is checkable fact. EvidenceStatus
	// records how well the conclusion is backed, set by the citation gate:
	//
	//   EvidenceGrounded    — at least one evidence quote verified verbatim
	//   EvidenceUnverified  — cites evidence but no quote matched the source
	//                         (paraphrase or fabrication risk)
	//   EvidenceUnsupported — no evidence cited at all
	//
	// It rides through debate, verification, and synthesis so unverified/
	// unsupported conclusions are caveated in the deliverable instead of being
	// stated as established fact — and so a grounded conclusion is NOT flagged
	// merely because its prose differs from the source. This lets cheap/local
	// models contribute analysis without their evidence either vanishing at the
	// gate or being passed off as verified.
	EvidenceStatus EvidenceStatus `json:"evidenceStatus,omitempty"`
	EvidenceNote   string         `json:"evidenceNote,omitempty"`
}

// EvidenceStatus classifies how well a finding's conclusion is backed by
// mechanically-verified source evidence.
type EvidenceStatus string

const (
	EvidenceGrounded    EvidenceStatus = "grounded"
	EvidenceUnverified  EvidenceStatus = "unverified"
	EvidenceUnsupported EvidenceStatus = "unsupported"
)

// ─── Human gates ─────────────────────────────────────────────────────────────

type GateRequest struct {
	ID           string  `json:"id"`
	TaskID       string  `json:"taskId"`
	FindingID    string  `json:"findingId"`
	Finding      Finding `json:"finding"`
	Status       string  `json:"status"`
	ReviewerNote string  `json:"reviewerNote,omitempty"`
	// ClientVoiceNote is Remy's read on this finding against the client's
	// stated goals/concerns (from the CNTXT advocacy brief). Shown to the
	// human reviewer alongside the finding.
	ClientVoiceNote string     `json:"clientVoiceNote,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	ReviewedAt      *time.Time `json:"reviewedAt,omitempty"`
}

// ─── Client voice (Remy / CNTXT advocacy) ─────────────────────────────────────

// ClientVoiceEntry is one advocacy observation captured by the client-facing
// agent: a goal, concern, constraint, or preference in the client's words.
type ClientVoiceEntry struct {
	Category string `json:"category"` // goal | concern | constraint | preference
	Note     string `json:"note"`
	At       string `json:"at,omitempty"`
}

// ClientVoice is the per-matter advocacy brief pushed by the client-facing
// agent (Remy). It travels with the matter and is surfaced at human gates.
type ClientVoice struct {
	MatterNumber string             `json:"matterNumber"`
	ClientID     string             `json:"clientId,omitempty"`
	Source       string             `json:"source"` // e.g. "remy"
	Entries      []ClientVoiceEntry `json:"entries"`
	UpdatedAt    time.Time          `json:"updatedAt"`
}

// MatterNotification is a message posted to a matter by an external agent
// or integration (Remy, CNTXT, bots). Fanned out to linked Teams/Slack
// channels when configured; always stored and audited.
type MatterNotification struct {
	ID           string    `json:"id"`
	MatterNumber string    `json:"matterNumber"`
	Source       string    `json:"source"`
	Message      string    `json:"message"`
	At           time.Time `json:"at"`
}

// ─── Task management ─────────────────────────────────────────────────────────

type WorkflowType string

const (
	WorkflowCounsel       WorkflowType = "counsel"
	WorkflowRoundtable    WorkflowType = "roundtable"
	WorkflowAdversarial   WorkflowType = "adversarial"
	WorkflowReview        WorkflowType = "review"
	WorkflowTabulate      WorkflowType = "tabulate"
	WorkflowFullBench     WorkflowType = "full_bench"
	WorkflowLegalDesign   WorkflowType = "legal_design"
	WorkflowPreEngagement WorkflowType = "pre_engagement"
)

type TaskStatus string

const (
	TaskStatusPending      TaskStatus = "pending"
	TaskStatusRunning      TaskStatus = "running"
	TaskStatusAwaitingGate TaskStatus = "awaiting_gate"
	TaskStatusComplete     TaskStatus = "complete"
	TaskStatusFailed       TaskStatus = "failed"
	// TaskStatusInterrupted marks a task that was persisted mid-run ("running"
	// or "awaiting_gate") and restored after a backend restart. Its runner
	// goroutine died with the previous process, so the task cannot make
	// progress: the boot-time quarantine sets this status (instead of silently
	// leaving it "running" forever) and the task must be explicitly
	// resubmitted. RESUME_RUNNING_TASKS=true restores the old behaviour.
	TaskStatusInterrupted TaskStatus = "interrupted"
)

type NosLegalTags struct {
	AreaOfLaw *string `json:"areaOfLaw,omitempty"`
	WorkType  *string `json:"workType,omitempty"`
	Sector    *string `json:"sector,omitempty"`
	AssetType *string `json:"assetType,omitempty"`
}

type TaskTable struct {
	Columns          []string            `json:"columns"`
	Rows             []map[string]string `json:"rows"`
	SourceFindingIDs []string            `json:"sourceFindingIds"`
	GeneratedAt      time.Time           `json:"generatedAt"`
}

type Task struct {
	ID                 string        `json:"id"`
	Description        string        `json:"description"`
	Jurisdiction       string        `json:"jurisdiction,omitempty"`
	ClientNumber       string        `json:"clientNumber,omitempty"`
	MatterNumber       string        `json:"matterNumber,omitempty"`
	AssignedLawyerIDs  []string      `json:"assignedLawyerIds,omitempty"`
	DocumentIDs        []string      `json:"documentIds"`
	CreatedByProfileID string        `json:"createdByProfileId,omitempty"`
	WorkflowType       WorkflowType  `json:"workflowType"`
	Status             TaskStatus    `json:"status"`
	CurrentPhase       TaskPhase     `json:"currentPhase"`
	CurrentRound       int           `json:"currentRound"`
	MaxRounds          int           `json:"maxRounds"`
	ActiveAgentIDs     []string      `json:"activeAgentIds"`
	Rounds             []RoundState  `json:"rounds"`
	Findings           []Finding     `json:"findings"`
	PendingGates       []GateRequest `json:"pendingGates"`
	Output             string        `json:"output,omitempty"`
	Error              string        `json:"error,omitempty"`
	CreatedAt          time.Time     `json:"createdAt"`
	UpdatedAt          time.Time     `json:"updatedAt"`
	CompletedAt        *time.Time    `json:"completedAt,omitempty"`
	Table              *TaskTable    `json:"table,omitempty"`
	NosLegal           *NosLegalTags `json:"noslegal,omitempty"`
	// Controversies are the cross-document conflicts surfaced by the reconciliation
	// analyst — graph-shaped, the seed for the future TypeDB contradiction graph.
	Controversies []Controversy `json:"controversies,omitempty"`
	// Allegations is the matter's distinct allegations/issues, enumerated ONCE (multi-
	// query, deduped) and shared by recruitment and the writer's coverage spine — so the
	// specialists recruited and the sections written cover the SAME set (a divergence
	// otherwise drops allegations from the deliverable that were found in the rounds).
	Allegations       []string `json:"allegations,omitempty"`
	ActiveTimeEntryID string   `json:"activeTimeEntryId,omitempty"`
	// StarvedRounds records every round that completed with zero findings from
	// all of its agents (see the round.starved audit event) — the signature of
	// model contention or round timeouts. Non-empty means the run was degraded:
	// consumers (UI, benchmark drivers) must not treat this task's output as a
	// full-pipeline result.
	StarvedRounds []StarvedRound `json:"starvedRounds,omitempty"`
}

// ─── Time tracking ───────────────────────────────────────────────────────────

type TimeEventType string

const (
	TimeEventTaskRun    TimeEventType = "task_run"
	TimeEventGateReview TimeEventType = "gate_review"
	TimeEventAgentWork  TimeEventType = "agent_work"
)

type TimeEntry struct {
	ID                string          `json:"id"`
	ProfileID         string          `json:"profileId,omitempty"`
	ProfileName       string          `json:"profileName,omitempty"`
	AgentID           string          `json:"agentId,omitempty"`
	AgentName         string          `json:"agentName,omitempty"`
	TaskID            string          `json:"taskId"`
	MatterNumber      string          `json:"matterNumber,omitempty"`
	ClientNumber      string          `json:"clientNumber,omitempty"`
	Description       string          `json:"description"`
	Event             TimeEventType   `json:"event"`
	StartedAt         time.Time       `json:"startedAt"`
	EndedAt           *time.Time      `json:"endedAt,omitempty"`
	DurationMs        int64           `json:"durationMs"`
	BillingUnits      int             `json:"billingUnits"`
	BillingRate       *float64        `json:"billingRate,omitempty"`
	BillingAmountUsd  *float64        `json:"billingAmountUsd,omitempty"`
	ClioSyncedAt      string          `json:"clioSyncedAt,omitempty"`
	UTBMSTaskCode     string          `json:"utbmsTaskCode,omitempty"`
	UTBMSActivityCode string          `json:"utbmsActivityCode,omitempty"`
	OcgSuggestions    []OcgSuggestion `json:"ocgSuggestions,omitempty"`
	OcgCheckedAt      string          `json:"ocgCheckedAt,omitempty"`
}

// ─── Lawyer profiles ──────────────────────────────────────────────────────────

type LawyerRole string

const (
	RoleLawyer  LawyerRole = "lawyer"
	RolePartner LawyerRole = "partner"
)

type UserMode string

const (
	ModeAdmin       UserMode = "admin"
	ModeFullFlavour UserMode = "full_flavour"
	ModeLite        UserMode = "lite"
)

// ModeColors is the hex accent colour for each mode (UI theming).
// Mirrors MODE_COLORS in src/types.ts.
var ModeColors = map[UserMode]string{
	ModeAdmin:       "#1A1A1A", // near-black (UI overrides to gold for visibility)
	ModeFullFlavour: "#C8102E", // scarlet
	ModeLite:        "#C4940F", // amber-gold
}

// ModeCapabilities carries feature flags with the session so the UI can
// conditionally render. Mirrors ModeCapabilities in src/types.ts.
type ModeCapabilities struct {
	ManageUsers     bool `json:"manageUsers"`
	SeeAllMatters   bool `json:"seeAllMatters"`
	AssignMatters   bool `json:"assignMatters"`
	ClientRoster    bool `json:"clientRoster"`
	TimeTracking    bool `json:"timeTracking"`
	MatterAnalytics bool `json:"matterAnalytics"`
	FullConnectors  bool `json:"fullConnectors"`
	AdminSettings   bool `json:"adminSettings"`
}

// ModeCapabilitySet mirrors MODE_CAPABILITIES in src/types.ts.
var ModeCapabilitySet = map[UserMode]ModeCapabilities{
	ModeAdmin: {
		ManageUsers: true, SeeAllMatters: true, AssignMatters: true,
		ClientRoster: true, TimeTracking: true, MatterAnalytics: true,
		FullConnectors: true, AdminSettings: true,
	},
	ModeFullFlavour: {
		ManageUsers: false, SeeAllMatters: false, AssignMatters: false,
		ClientRoster: true, TimeTracking: true, MatterAnalytics: false,
		FullConnectors: true, AdminSettings: false,
	},
	ModeLite: {
		ManageUsers: false, SeeAllMatters: false, AssignMatters: false,
		ClientRoster: false, TimeTracking: false, MatterAnalytics: false,
		FullConnectors: false, AdminSettings: false,
	},
}

type ToneProfile struct {
	GeneratedAt       string   `json:"generatedAt"`
	SourceType        string   `json:"sourceType"`
	SampleCount       int      `json:"sampleCount"`
	Formality         string   `json:"formality"`
	SentenceStyle     string   `json:"sentenceStyle"`
	Vocabulary        string   `json:"vocabulary"`
	RhetoricalStyle   string   `json:"rhetoricalStyle"`
	SignaturePatterns []string `json:"signaturePatterns"`
	InjectionSnippet  string   `json:"injectionSnippet"`
}

type LawyerProfile struct {
	ID                 string       `json:"id"`
	Name               string       `json:"name"`
	Email              string       `json:"email"`
	Role               LawyerRole   `json:"role"`
	Mode               UserMode     `json:"mode,omitempty"`
	Title              string       `json:"title,omitempty"`
	Color              string       `json:"color,omitempty"`
	OAuthSubject       string       `json:"oauthSubject,omitempty"`
	PracticeAreas      []string     `json:"practiceAreas,omitempty"`
	Bio                string       `json:"bio,omitempty"`
	LinkedInProfileURL string       `json:"linkedinProfileUrl,omitempty"`
	ToneProfile        *ToneProfile `json:"toneProfile,omitempty"`
	CreatedAt          time.Time    `json:"createdAt"`
}

type SessionUser struct {
	ProfileID string     `json:"profileId"`
	Name      string     `json:"name"`
	Email     string     `json:"email"`
	Role      LawyerRole `json:"role"`
	Mode      UserMode   `json:"mode"`
}

// ─── Clients ─────────────────────────────────────────────────────────────────

type ClientMatter struct {
	MatterNumber          string    `json:"matterNumber"`
	Description           string    `json:"description"`
	PracticeArea          string    `json:"practiceArea,omitempty"`
	OpenedAt              time.Time `json:"openedAt"`
	BudgetUsd             *float64  `json:"budgetUsd,omitempty"`
	BudgetAlertThresholds []float64 `json:"budgetAlertThresholds,omitempty"`
	BudgetAlertsTriggered []float64 `json:"budgetAlertsTriggered,omitempty"`
}

type Client struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	ClientNumber string         `json:"clientNumber"`
	Matters      []ClientMatter `json:"matters"`
	Adversaries  []string       `json:"adversaries"`
	Notes        string         `json:"notes,omitempty"`
	CreatedAt    time.Time      `json:"createdAt"`
	UpdatedAt    time.Time      `json:"updatedAt"`
}

type ConflictCheckResult struct {
	HasConflict           bool   `json:"hasConflict"`
	ConflictingClientID   string `json:"conflictingClientId,omitempty"`
	ConflictingClientName string `json:"conflictingClientName,omitempty"`
	MatchedAdversary      string `json:"matchedAdversary,omitempty"`
}

// ConflictReport is a single inferred conflict edge from the graph.
type ConflictReport struct {
	ClientAID     string `json:"clientAId"`
	ClientAName   string `json:"clientAName"`
	ClientBID     string `json:"clientBId"`
	ClientBName   string `json:"clientBName"`
	MatterANumber string `json:"matterANumber"`
	MatterBNumber string `json:"matterBNumber"`
	ConflictPath  string `json:"conflictPath"`
	DetectedAt    string `json:"detectedAt"`
}

// ─── Documents ───────────────────────────────────────────────────────────────

type Document struct {
	ID                   string                 `json:"id"`
	Title                string                 `json:"title"`
	Content              string                 `json:"content"`
	Source               string                 `json:"source,omitempty"`
	Jurisdiction         string                 `json:"jurisdiction,omitempty"`
	DocumentType         string                 `json:"documentType,omitempty"`
	OwnerID              string                 `json:"ownerId,omitempty"`
	PracticeArea         string                 `json:"practiceArea,omitempty"`
	DetectedClientNumber string                 `json:"detectedClientNumber,omitempty"`
	NosLegal             *NosLegalTags          `json:"noslegal,omitempty"`
	Metadata             map[string]interface{} `json:"metadata,omitempty"`
	Embedding            []float32              `json:"-"`
	IngestedAt           time.Time              `json:"ingestedAt"`
	// Attachments are the document's retained binary artifacts (the original
	// uploaded image/PDF, rendered pages, embedded figures). Bytes live in the
	// blob store; this carries only metadata, populated on read for the API.
	Attachments []Attachment `json:"attachments,omitempty"`
}

// AttachmentKind classifies a stored binary artifact.
type AttachmentKind string

const (
	AttachmentOriginal AttachmentKind = "original" // the file as uploaded
	AttachmentEmbedded AttachmentKind = "embedded" // an image to place into output
)

// Attachment is a binary artifact retained alongside a document. Metadata
// persists in the durable store (RLS-scoped by OwnerID); the bytes live in the
// blob store keyed by BlobKey.
type Attachment struct {
	ID        string         `json:"id"`
	DocID     string         `json:"docId"`
	OwnerID   string         `json:"ownerId,omitempty"`
	Filename  string         `json:"filename"`
	MediaType string         `json:"mediaType"`
	Kind      AttachmentKind `json:"kind"`
	Size      int64          `json:"size"`
	BlobKey   string         `json:"-"` // storage key; not exposed in the API
	Page      int            `json:"page,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
}

type SearchResult struct {
	Document Document `json:"document"`
	Score    float64  `json:"score"`
	Excerpt  string   `json:"excerpt"`
}

// ─── Practice areas ──────────────────────────────────────────────────────────

var PracticeAreas = []string{
	"Corporate & M&A",
	"Competition & Antitrust",
	"Employment & Labour",
	"Intellectual Property",
	"Real Estate",
	"Banking & Finance",
	"Litigation & Dispute Resolution",
	"Tax",
	"Regulatory & Compliance",
	"Data Privacy & Cybersecurity",
	"Immigration",
	"Insolvency & Restructuring",
	"Capital Markets",
	"Insurance",
	"Environmental & Climate",
}

// ─── Task template ────────────────────────────────────────────────────────────

type TaskTemplate struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	WorkflowType   WorkflowType      `json:"workflowType"`
	PromptTemplate string            `json:"promptTemplate"`
	Substitutions  map[string]string `json:"substitutions,omitempty"`
}

// ─── Citation validity (KeyCite / Shepard's replacement) ─────────────────────

type CitationSignal string
type CitationStatus string
type CitationTreatmentType string

const (
	CitationSignalGreen  CitationSignal = "green"
	CitationSignalYellow CitationSignal = "yellow"
	CitationSignalRed    CitationSignal = "red"
	CitationSignalBlue   CitationSignal = "blue"
)

type CitationTreatment struct {
	CaseName      string `json:"caseName"`
	Citation      string `json:"citation,omitempty"`
	TreatmentType string `json:"treatmentType"`
	Court         string `json:"court,omitempty"`
	Year          *int   `json:"year,omitempty"`
	URL           string `json:"url,omitempty"`
}

type CitationCheckResult struct {
	Query                  string              `json:"query"`
	ResolvedCitation       string              `json:"resolvedCitation,omitempty"`
	ClusterID              string              `json:"clusterId,omitempty"`
	CaseName               string              `json:"caseName,omitempty"`
	Court                  string              `json:"court,omitempty"`
	Year                   *int                `json:"year,omitempty"`
	Status                 CitationStatus      `json:"status"`
	Signal                 CitationSignal      `json:"signal"`
	SignalLabel            string              `json:"signalLabel"`
	Confidence             float64             `json:"confidence"`
	PositiveTreatmentCount int                 `json:"positiveTreatmentCount"`
	NegativeTreatmentCount int                 `json:"negativeTreatmentCount"`
	TopNegativeTreatments  []CitationTreatment `json:"topNegativeTreatments"`
	Reasoning              string              `json:"reasoning"`
	CourtListenerURL       string              `json:"courtListenerUrl,omitempty"`
	CheckedAt              string              `json:"checkedAt"`
	CheckedBy              string              `json:"checkedBy"`
}

// ─── Matter health ────────────────────────────────────────────────────────────

type HealthSignal string
type HealthTrend string

const (
	HealthGreen HealthSignal = "green"
	HealthAmber HealthSignal = "amber"
	HealthRed   HealthSignal = "red"
)

type MatterRiskFactor struct {
	Type            string `json:"type"`
	Severity        string `json:"severity"`
	Message         string `json:"message"`
	SuggestedAction string `json:"suggestedAction,omitempty"`
}

type MatterHealthDimensions struct {
	BudgetHealth      float64 `json:"budgetHealth"`
	DeadlineHealth    float64 `json:"deadlineHealth"`
	ActivityFreshness float64 `json:"activityFreshness"`
	GateBacklog       float64 `json:"gateBacklog"`
	OcgCompliance     float64 `json:"ocgCompliance"`
}

type MatterHealthScore struct {
	MatterNumber string                 `json:"matterNumber"`
	Score        float64                `json:"score"`
	Signal       HealthSignal           `json:"signal"`
	SignalLabel  string                 `json:"signalLabel"`
	Dimensions   MatterHealthDimensions `json:"dimensions"`
	RiskFactors  []MatterRiskFactor     `json:"riskFactors"`
	Trend        HealthTrend            `json:"trend"`
	ComputedAt   string                 `json:"computedAt"`
}

type PortfolioHealthSummary struct {
	TotalMatters int                 `json:"totalMatters"`
	Green        int                 `json:"green"`
	Amber        int                 `json:"amber"`
	Red          int                 `json:"red"`
	Matters      []MatterHealthScore `json:"matters"`
	ComputedAt   string              `json:"computedAt"`
}

// ─── Playbook ─────────────────────────────────────────────────────────────────

type PlaybookScope string

const (
	PlaybookScopeFirm     PlaybookScope = "firm"
	PlaybookScopeClient   PlaybookScope = "client"
	PlaybookScopeMatter   PlaybookScope = "matter"
	PlaybookScopePersonal PlaybookScope = "personal"
)

type PlaybookEntry struct {
	ClauseType          string   `json:"clauseType"`
	PracticeArea        string   `json:"practiceArea"`
	StandardPosition    string   `json:"standardPosition"`
	FallbackPosition    string   `json:"fallbackPosition,omitempty"`
	RedLines            []string `json:"redLines"`
	DealPoints          []string `json:"dealPoints"`
	SourceDocumentCount int      `json:"sourceDocumentCount"`
	ExampleLanguage     []string `json:"exampleLanguage,omitempty"`
	LastUpdated         string   `json:"lastUpdated"`
}

type Playbook struct {
	ID                string          `json:"id"`
	Scope             PlaybookScope   `json:"scope"`
	OwnerID           string          `json:"ownerId,omitempty"`
	OwnerName         string          `json:"ownerName,omitempty"`
	Name              string          `json:"name"`
	Description       string          `json:"description,omitempty"`
	PracticeArea      string          `json:"practiceArea"`
	Jurisdiction      string          `json:"jurisdiction,omitempty"`
	ClauseTypes       []string        `json:"clauseTypes"`
	Entries           []PlaybookEntry `json:"entries"`
	DocumentCount     int             `json:"documentCount"`
	CreatedAt         string          `json:"createdAt"`
	UpdatedAt         string          `json:"updatedAt"`
	GeneratedByTaskID string          `json:"generatedByTaskId,omitempty"`
}

// ─── Invoice validation ───────────────────────────────────────────────────────

type InvoiceLineItem struct {
	LineID          string   `json:"lineId"`
	Date            string   `json:"date,omitempty"`
	TimekeeperName  string   `json:"timekeeperName,omitempty"`
	TimekeeperClass string   `json:"timekeeperClass,omitempty"`
	TaskCode        string   `json:"taskCode,omitempty"`
	ActivityCode    string   `json:"activityCode,omitempty"`
	Description     string   `json:"description"`
	Hours           *float64 `json:"hours,omitempty"`
	Rate            *float64 `json:"rate,omitempty"`
	Amount          *float64 `json:"amount,omitempty"`
}

type InvoiceViolation struct {
	LineID             string   `json:"lineId"`
	RuleID             string   `json:"ruleId,omitempty"`
	RuleText           string   `json:"ruleText,omitempty"`
	Type               string   `json:"type"`
	Severity           string   `json:"severity"`
	Message            string   `json:"message"`
	SuggestedAction    string   `json:"suggestedAction"`
	SuggestedReduction *float64 `json:"suggestedReduction,omitempty"`
}

type InvoiceValidationResult struct {
	ID                      string             `json:"id"`
	ClientID                string             `json:"clientId,omitempty"`
	SubmittedByFirm         string             `json:"submittedByFirm,omitempty"`
	MatterNumber            string             `json:"matterNumber,omitempty"`
	TotalOriginalAmount     float64            `json:"totalOriginalAmount"`
	TotalSuggestedReduction float64            `json:"totalSuggestedReduction"`
	TotalApprovedAmount     float64            `json:"totalApprovedAmount"`
	LineCount               int                `json:"lineCount"`
	ViolationCount          int                `json:"violationCount"`
	HardViolationCount      int                `json:"hardViolationCount"`
	Violations              []InvoiceViolation `json:"violations"`
	DisputeLetter           string             `json:"disputeLetter,omitempty"`
	ValidatedAt             string             `json:"validatedAt"`
}

// ─── Headnotes ────────────────────────────────────────────────────────────────

type Headnote struct {
	Number                int      `json:"number"`
	Proposition           string   `json:"proposition"`
	SourceText            string   `json:"sourceText"`
	Location              string   `json:"location,omitempty"`
	HoldingType           string   `json:"holdingType"`
	DistinguishingFactors []string `json:"distinguishingFactors"`
	AreaOfLaw             string   `json:"areaOfLaw,omitempty"`
	Confidence            float64  `json:"confidence"`
}

type HeadnoteReport struct {
	ID                string     `json:"id"`
	CaseName          string     `json:"caseName"`
	Citation          string     `json:"citation,omitempty"`
	Court             string     `json:"court,omitempty"`
	DateFiled         string     `json:"dateFiled,omitempty"`
	Jurisdiction      string     `json:"jurisdiction,omitempty"`
	KeyHolding        string     `json:"keyHolding"`
	Headnotes         []Headnote `json:"headnotes"`
	RelatedPrinciples []string   `json:"relatedPrinciples"`
	PracticeAreas     []string   `json:"practiceAreas"`
	NosLegalArea      string     `json:"noslegalArea,omitempty"`
	TotalHeadnotes    int        `json:"totalHeadnotes"`
	RatioCount        int        `json:"ratioCount"`
	ObiterCount       int        `json:"obiterCount"`
	GeneratedAt       string     `json:"generatedAt"`
}

// ─── OCG (Outside Counsel Guidelines) ────────────────────────────────────────

type OcgRuleCategory string

const (
	OcgCategoryBillingIncrements OcgRuleCategory = "billing_increments"
	OcgCategoryEntrySpecificity  OcgRuleCategory = "entry_specificity"
	OcgCategoryProhibitedTasks   OcgRuleCategory = "prohibited_tasks"
	OcgCategoryRateLimits        OcgRuleCategory = "rate_limits"
	OcgCategoryStaffing          OcgRuleCategory = "staffing"
	OcgCategoryDescriptionFormat OcgRuleCategory = "description_format"
	OcgCategoryTiming            OcgRuleCategory = "timing"
	OcgCategoryOther             OcgRuleCategory = "other"
)

type OcgMechCheckType string

const (
	OcgMechMinDurationHours    OcgMechCheckType = "min_duration_hours"
	OcgMechMaxDurationHours    OcgMechCheckType = "max_duration_hours"
	OcgMechMaxAgeDays          OcgMechCheckType = "max_age_days"
	OcgMechMaxBillingRateUSD   OcgMechCheckType = "max_billing_rate_usd"
	OcgMechMinDescriptionChars OcgMechCheckType = "min_description_chars"
	OcgMechNoBlockBilling      OcgMechCheckType = "no_block_billing"
	OcgMechNoVagueEntries      OcgMechCheckType = "no_vague_entries"
	OcgMechRequireMatterRef    OcgMechCheckType = "require_matter_reference"
)

type OcgMechCheck struct {
	Type  OcgMechCheckType `json:"type"`
	Value *float64         `json:"value,omitempty"`
}

type OcgRuleStat struct {
	Violations int `json:"violations"`
	Accepted   int `json:"accepted"`
	Dismissed  int `json:"dismissed"`
}

type OcgRule struct {
	ID        string          `json:"id"`
	Category  OcgRuleCategory `json:"category"`
	Text      string          `json:"text"`
	Severity  string          `json:"severity"`
	MechCheck *OcgMechCheck   `json:"mechCheck,omitempty"`
}

type OcgDocument struct {
	ID        string                  `json:"id"`
	ClientID  string                  `json:"clientId"`
	Title     string                  `json:"title"`
	Rules     []OcgRule               `json:"rules"`
	Excerpt   string                  `json:"excerpt,omitempty"`
	RuleStats map[string]*OcgRuleStat `json:"ruleStats,omitempty"`
	CreatedAt time.Time               `json:"createdAt"`
	UpdatedAt time.Time               `json:"updatedAt"`
}

type OcgSuggestion struct {
	RuleID               string          `json:"ruleId"`
	RuleText             string          `json:"ruleText"`
	Category             OcgRuleCategory `json:"category"`
	Severity             string          `json:"severity"`
	Issue                string          `json:"issue"`
	SuggestedDescription string          `json:"suggestedDescription,omitempty"`
	Status               string          `json:"status"`
}

// ─── Job queue ────────────────────────────────────────────────────────────────

type JobType string

const (
	JobTypeSummarizeTimeEntry JobType = "summarize_time_entry"
	JobTypeOcgBulkCheck       JobType = "ocg_bulk_check"
	JobTypeLPMStatusReport    JobType = "lpm_status_report"
	JobTypeLPMPortfolio       JobType = "lpm_portfolio_briefing"
	JobTypeLPMBackfill        JobType = "lpm_email_backfill"
)

type JobStatus string

const (
	JobStatusPending    JobStatus = "pending"
	JobStatusRunning    JobStatus = "running"
	JobStatusDone       JobStatus = "done"
	JobStatusFailed     JobStatus = "failed"
	JobStatusDeadLetter JobStatus = "dead_letter"
)

type Job struct {
	ID          string                 `json:"id"`
	Type        JobType                `json:"type"`
	Payload     map[string]interface{} `json:"payload"`
	Status      JobStatus              `json:"status"`
	CreatedAt   string                 `json:"createdAt"`
	StartedAt   string                 `json:"startedAt,omitempty"`
	CompletedAt string                 `json:"completedAt,omitempty"`
	Retries     int                    `json:"retries"`
	MaxRetries  int                    `json:"maxRetries"`
	Error       string                 `json:"error,omitempty"`
}

// ─── Dockets ──────────────────────────────────────────────────────────────────

type WatchedDocket struct {
	MatterNumber     string `json:"matterNumber"`
	DocketNumber     string `json:"docketNumber"`
	Court            string `json:"court"`
	CaseName         string `json:"caseName,omitempty"`
	AddedAt          string `json:"addedAt"`
	LastCheckedAt    string `json:"lastCheckedAt,omitempty"`
	LastFilingDate   string `json:"lastFilingDate,omitempty"`
	TotalFilingsSeen int    `json:"totalFilingsSeen"`
}

type DocketAlert struct {
	ID               string `json:"id"`
	MatterNumber     string `json:"matterNumber"`
	DocketNumber     string `json:"docketNumber"`
	Court            string `json:"court"`
	CaseName         string `json:"caseName"`
	NewFilingCount   int    `json:"newFilingCount"`
	LatestFilingDate string `json:"latestFilingDate"`
	CourtListenerURL string `json:"courtListenerUrl"`
	DetectedAt       string `json:"detectedAt"`
}

// ─── Regulatory pulse ─────────────────────────────────────────────────────────

type RegulationAlert struct {
	ID           string `json:"id"`
	MatterNumber string `json:"matterNumber,omitempty"`
	PracticeArea string `json:"practiceArea"`
	Jurisdiction string `json:"jurisdiction"`
	Headline     string `json:"headline"`
	URL          string `json:"url"`
	Summary      string `json:"summary"`
	DetectedAt   string `json:"detectedAt"`
	Source       string `json:"source"`
}

// ─── Client briefing ─────────────────────────────────────────────────────────

type BriefingMatterSnapshot struct {
	MatterNumber      string  `json:"matterNumber"`
	Description       string  `json:"description"`
	PracticeArea      string  `json:"practiceArea,omitempty"`
	Status            string  `json:"status"`
	DaysSinceActivity int     `json:"daysSinceActivity"`
	OpenBillingUsd    float64 `json:"openBillingUsd"`
	TotalBilledUsd    float64 `json:"totalBilledUsd"`
	PendingGates      int     `json:"pendingGates"`
	LastOutput        string  `json:"lastOutput,omitempty"`
}

type BriefingBillingSnapshot struct {
	Last90DaysUsd   float64 `json:"last90DaysUsd"`
	WipUsd          float64 `json:"wipUsd"`
	OldestWipDays   int     `json:"oldestWipDays"`
	OpenMatterCount int     `json:"openMatterCount"`
}

type ClientBriefing struct {
	ID                string                   `json:"id"`
	ClientID          string                   `json:"clientId"`
	ClientName        string                   `json:"clientName"`
	ClientNumber      string                   `json:"clientNumber"`
	GeneratedAt       string                   `json:"generatedAt"`
	BriefingDate      string                   `json:"briefingDate"`
	ExecutiveSummary  string                   `json:"executiveSummary"`
	Matters           []BriefingMatterSnapshot `json:"matters"`
	Billing           BriefingBillingSnapshot  `json:"billing"`
	OpenItems         []string                 `json:"openItems"`
	RelationshipNotes string                   `json:"relationshipNotes,omitempty"`
	IndustryContext   string                   `json:"industryContext,omitempty"`
	Document          string                   `json:"document"`
}

// ─── Budget ───────────────────────────────────────────────────────────────────

type BudgetAlert struct {
	MatterNumber string  `json:"matterNumber"`
	ClientNumber string  `json:"clientNumber"`
	BudgetUsd    float64 `json:"budgetUsd"`
	BurnUsd      float64 `json:"burnUsd"`
	BurnPct      float64 `json:"burnPct"`
	Threshold    float64 `json:"threshold"`
	TriggeredAt  string  `json:"triggeredAt"`
}

type BudgetBurn struct {
	BudgetUsd float64 `json:"budgetUsd"`
	BurnUsd   float64 `json:"burnUsd"`
	BurnPct   float64 `json:"burnPct"`
	Remaining float64 `json:"remaining"`
}

type BudgetPrediction struct {
	MatterNumber          string  `json:"matterNumber"`
	PracticeArea          string  `json:"practiceArea"`
	SpentUsd              float64 `json:"spentUsd"`
	SpentBillingUnits     int     `json:"spentBillingUnits"`
	EstimatedTotalUsd     float64 `json:"estimatedTotalUsd"`
	EstimatedRemainingUsd float64 `json:"estimatedRemainingUsd"`
	CompletionPct         float64 `json:"completionPct"`
	Confidence            string  `json:"confidence"`
	ComparableMatterCount int     `json:"comparableMatterCount"`
	MedianFinalCost       float64 `json:"medianFinalCost"`
	P25FinalCost          float64 `json:"p25FinalCost"`
	P75FinalCost          float64 `json:"p75FinalCost"`
	BasedOn               string  `json:"basedOn"`
}

// ─── Status reports ───────────────────────────────────────────────────────────

type StatusReport struct {
	TaskID       string  `json:"taskId"`
	MatterNumber string  `json:"matterNumber,omitempty"`
	ClientNumber string  `json:"clientNumber,omitempty"`
	GeneratedAt  string  `json:"generatedAt"`
	Format       string  `json:"format"`
	Content      string  `json:"content"`
	WordCount    int     `json:"wordCount"`
	CostUsd      float64 `json:"costUsd"`
}

// ─── LPM: daily matter status reports ──────────────────────────────────────────
//
// MatterStatusReport is the structured, machine-readable daily status report for
// a single matter — the single source of truth that the JSON, Markdown and DOCX
// renderers all consume. Reports accumulate, one per matter per day, into an
// append-only corpus that becomes a mineable time-series over the life of a deal.

type LPMWorkstream struct {
	Name     string `json:"name"`
	Status   string `json:"status"` // e.g. "on track", "blocked", "at risk"
	Owner    string `json:"owner,omitempty"`
	NextStep string `json:"nextStep,omitempty"`
	DueDate  string `json:"dueDate,omitempty"`
}

type LPMRisk struct {
	Severity          string `json:"severity"` // "low" | "medium" | "high"
	Description       string `json:"description"`
	RecommendedAction string `json:"recommendedAction,omitempty"`
}

// LPMDeltas are the deterministic, machine-computed changes since the previous
// report (or the trailing 24h when this is the first report for the matter).
type LPMDeltas struct {
	Since             string   `json:"since"` // RFC3339 cutoff the deltas are measured from
	NewTasks          int      `json:"newTasks"`
	ClosedTasks       int      `json:"closedTasks"`
	NewFindings       int      `json:"newFindings"`
	EmailsRouted      int      `json:"emailsRouted"` // populated by the Phase 2 email router
	DeadlinesUpcoming []string `json:"deadlinesUpcoming,omitempty"`
	BudgetBurnPct     float64  `json:"budgetBurnPct"`
	HoursLogged       float64  `json:"hoursLogged"`
	BilledUsd         float64  `json:"billedUsd"`
}

type MatterStatusReport struct {
	ReportID      string          `json:"reportId"`
	MatterNumber  string          `json:"matterNumber"`
	ClientNumber  string          `json:"clientNumber,omitempty"`
	Date          string          `json:"date"`        // YYYY-MM-DD — the report's logical day
	GeneratedAt   string          `json:"generatedAt"` // RFC3339
	GeneratedBy   string          `json:"generatedBy"` // model id
	PrevReportID  string          `json:"prevReportId,omitempty"`
	HealthScore   float64         `json:"healthScore"`
	HealthSignal  string          `json:"healthSignal"`
	HealthTrend   string          `json:"healthTrend,omitempty"`
	BLUF          string          `json:"bluf"` // bottom-line-up-front, partner-digestible in seconds
	Summary       string          `json:"summary"`
	Workstreams   []LPMWorkstream `json:"workstreams,omitempty"`
	Risks         []LPMRisk       `json:"risks,omitempty"`
	OpenQuestions []string        `json:"openQuestions,omitempty"`
	Deltas        LPMDeltas       `json:"deltas"`
	Sources       []string        `json:"sources,omitempty"`
	Confidence    float64         `json:"confidence"`
	CostUsd       float64         `json:"costUsd"`
}

// ─── Deadlines ────────────────────────────────────────────────────────────────

type DayType string

const (
	DayTypeCalendar DayType = "calendar"
	DayTypeBusiness DayType = "business"
)

type DeadlineRule struct {
	ID          string  `yaml:"id"`
	Trigger     string  `yaml:"trigger"`
	Event       string  `yaml:"event"`
	Days        int     `yaml:"days"`
	DayType     DayType `yaml:"dayType"`
	Cite        string  `yaml:"cite"`
	Note        string  `yaml:"note,omitempty"`
	WarningDays int     `yaml:"warningDays,omitempty"`
}

type HolidayCalendar string

const (
	HolidaysUSFederal      HolidayCalendar = "us_federal"
	HolidaysUKBank         HolidayCalendar = "uk_bank"
	HolidaysEUInstitutions HolidayCalendar = "eu_institutions"
	HolidaysNone           HolidayCalendar = "none"
)

type JurisdictionRules struct {
	ID           string          `yaml:"id"`
	Jurisdiction string          `yaml:"jurisdiction"`
	Name         string          `yaml:"name"`
	Version      string          `yaml:"version"`
	Source       string          `yaml:"source,omitempty"`
	Holidays     HolidayCalendar `yaml:"holidays"`
	Rules        []DeadlineRule  `yaml:"rules"`
}

type ComputedDeadline struct {
	RuleID      string  `json:"ruleId"`
	Event       string  `json:"event"`
	DueDate     string  `json:"dueDate"`
	WarningDate string  `json:"warningDate,omitempty"`
	Days        int     `json:"days"`
	DayType     DayType `json:"dayType"`
	Cite        string  `json:"cite"`
	Note        string  `json:"note,omitempty"`
}

type DeadlineResult struct {
	Jurisdiction     string             `json:"jurisdiction"`
	JurisdictionName string             `json:"jurisdictionName"`
	TriggerEvent     string             `json:"triggerEvent"`
	TriggerDate      string             `json:"triggerDate"`
	ComputedAt       string             `json:"computedAt"`
	Deadlines        []ComputedDeadline `json:"deadlines"`
}
