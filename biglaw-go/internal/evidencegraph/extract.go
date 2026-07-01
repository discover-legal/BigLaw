// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

package evidencegraph

import (
	"encoding/json"
	"strings"

	"github.com/discover-legal/biglaw-go/internal/providers"
)

// Chatter is the minimal model surface the extractor needs (providers.Provider satisfies it).
type Chatter interface {
	Chat(providers.ChatParams) (*providers.ChatResponse, error)
}

// Two-pass, entity-anchored extraction. The probe showed naive single-pass relation
// extraction silently drops facts buried in parentheticals and "did not disclose / failed
// to" clauses (e.g. "Kevin Ostrowski (a 40% owner of Lakeshore Trading)"). Pass 1 pulls
// entities WITH their attributes, explicitly parenthetical/omission-aware; Pass 2 pulls
// relations among them. Both are grounded at Add time (quote must be verbatim).

const entitySystem = "You extract entities for an evidence graph from legal text. List EVERY named party, person, firm, fund, account, or entity, with any attribute attached (ownership %, role, title, amount, stake, relationship). CRITICAL: include entities and attributes that appear INSIDE parentheticals and inside 'did not disclose', 'failed to', 'omitted', or other negative/omission clauses — an omission is itself an allegation and the parties and figures named within it are facts. Respond with ONLY a JSON array; each element: {\"entity\":\"<name>\",\"attribute\":\"<role/ownership/stake/relationship, with any figure or percent, or empty>\",\"value\":\"<the figure/percent/date if any, else empty>\",\"quote\":\"<the verbatim span from the passage that states it>\"}."

const relationSystem = "You extract relationships for an evidence graph from legal text. For each relationship the passage STATES between two named parties/entities/funds/accounts, output a typed triple. Include relationships stated inside parentheticals and 'did not disclose / failed to / omitted' clauses. Respond with ONLY a JSON array; each element: {\"subject\":\"<name>\",\"relation\":\"<verb phrase, e.g. holds 12% interest in / is 40% owner of / received undisclosed compensation from / is victim of / is employee of / is portfolio manager of>\",\"object\":\"<name>\",\"value\":\"<figure/percent/date if attached, else empty>\",\"quote\":\"<verbatim span proving it>\"}."

const allegationSystem = "You identify the DISTINCT allegations, claims, charges, schemes, or required topics this passage raises, as short section headings — be exhaustive, include secondary and party-specific ones, and prefer the document's own enumeration where it numbers or names them. Respond with ONLY a JSON array; each element: {\"label\":\"<short heading, max ~8 words, the matter's own terms>\",\"quote\":\"<verbatim span from the passage evidencing this allegation>\"}."

// tripleSystem extracts typed BELO triples (controlled predicates + node classes). The
// Conduct-centric predicates (committedBy/violates/harmed/quantifiedAs) make each wrongful
// scheme a Conduct node — the spine is then DISCOVERED from these, not enumerated. Neutral/
// policy/review text is skipped (no controlled predicate fits), which is what keeps Form-ADV
// triggers and compliance-review activities out of the allegation graph.
const tripleSystem = "Extract RDF triples for a legal evidence graph. An ISSUE is a distinct proposition this deliverable must ASSESS — depending on the matter, a NAMED alleged wrongful scheme/violation (enforcement), OR a NAMED client requirement/instruction/standard to be met (compliance or comparing documents against instructions), OR a NAMED contract clause/obligation (transactional). Use the matter's own names. Use ONLY these predicates. ENFORCEMENT (subject is a Conduct): committedBy (Conduct->Party), violates (Conduct->Authority/Rule/Statute), harmed (Conduct->Party), quantifiedAs (Conduct or Party->Quantity), directedTradesTo (Party->Broker), alteredRecords (Party->Instrument), occurredDuring (Conduct->Event). COMPLIANCE / COMPARISON / TRANSACTIONAL (subject is a Requirement or Clause): requires (Requirement->thing the instruction/standard calls for), satisfiedBy (Requirement->Instrument that meets it), deviatesFrom (Requirement->thing where the draft departs, omits, or conflicts), prohibits (Requirement->thing). PARTIES: ownsStakeIn (Party->Party), receivedFrom (Party->Party), failedToDisclose (Party->thing), heldAccountAt (Party->Broker). For EACH triple output {\"s\",\"sClass\",\"p\",\"o\",\"oClass\",\"value\" (a figure if attached, else \"\"),\"quote\" (verbatim span from the passage)}. The sClass of an issue node MUST be one of: Conduct, Requirement, Clause. Other classes: Party, Person, Firm, Fund, Account, Broker, Client, Authority, Instrument, Quantity, Event. Use ONLY the listed predicates; SKIP neutral background and boilerplate. Output ONLY a JSON array."

type tripleRow struct {
	S, SClass, P, O, OClass, Value, Quote string
}

type entityRow struct {
	Entity, Attribute, Value, Quote string
}
type relationRow struct {
	Subject, Relation, Object, Value, Quote string
}
type allegationRow struct {
	Label, Quote string
}

// ExtractInto runs both passes over one chunk and adds every grounded fact to g. Returns
// (kept, rejected) counts so callers can log the grounding ratio. Best-effort: a model or
// parse error on either pass is swallowed (the other pass still contributes).
func ExtractInto(g *Graph, chat Chatter, model string, temperature *float64, chunk, source string) (int, int) {
	kept, rej := 0, 0
	add := func(f Fact) {
		if g.Add(f, chunk) {
			kept++
		} else {
			rej++
		}
	}

	// Pass 1 — entities + attributes (parenthetical/omission-aware).
	if text, ok := chatJSON(chat, model, temperature, entitySystem, chunk); ok {
		var rows []entityRow
		if parseJSONArray(text, &rows) {
			for _, r := range rows {
				if strings.TrimSpace(r.Attribute) == "" && strings.TrimSpace(r.Value) == "" {
					continue // a bare entity with no attribute is not yet a fact
				}
				add(Fact{Subject: r.Entity, Relation: r.Attribute, Value: r.Value, Quote: r.Quote, Source: source})
			}
		}
	}

	// Pass 2 — relations among entities.
	if text, ok := chatJSON(chat, model, temperature, relationSystem, chunk); ok {
		var rows []relationRow
		if parseJSONArray(text, &rows) {
			for _, r := range rows {
				add(Fact{Subject: r.Subject, Relation: r.Relation, Object: r.Object, Value: r.Value, Quote: r.Quote, Source: source})
			}
		}
	}

	// Pass 3 — distinct allegations (grounded), legacy label set kept for the enumeration spine.
	if text, ok := chatJSON(chat, model, temperature, allegationSystem, chunk); ok {
		var rows []allegationRow
		if parseJSONArray(text, &rows) {
			for _, r := range rows {
				g.AddAllegation(r.Label, r.Quote, chunk)
			}
		}
	}

	return kept, rej
}

// ExtractTriplesInto runs ONLY the typed BELO-triple pass (controlled predicates + node
// classes) over one chunk, populating the Claim graph's Conduct nodes — from which the spine is
// DISCOVERED (g.Conducts()). Split from ExtractInto so it can run on a STRONGER model than the
// 7B bulk: conducts are document-level abstractions the 7B mislabels (firm-as-Conduct, empty
// quotes), so the spine pass is routed to a capable model. AddTriple keeps only
// ontology-recognized, domain/range-valid triples (re-orienting reversed ones).
func ExtractTriplesInto(g *Graph, chat Chatter, model string, temperature *float64, chunk, source string) (int, int) {
	kept, rej := 0, 0
	if text, ok := chatJSON(chat, model, temperature, tripleSystem, chunk); ok {
		var rows []tripleRow
		if parseJSONArray(text, &rows) {
			for _, r := range rows {
				if g.AddTriple(r.S, r.SClass, r.P, r.O, r.OClass, r.Value, r.Quote, source, chunk) {
					kept++
				} else {
					rej++
				}
			}
		}
	}
	return kept, rej
}

func chatJSON(chat Chatter, model string, temperature *float64, system, user string) (string, bool) {
	resp, err := chat.Chat(providers.ChatParams{
		Model:       model,
		MaxTokens:   1500,
		System:      system,
		Messages:    []providers.Message{{Role: "user", Content: "PASSAGE:\n" + user}},
		CacheSystem: true,
		Temperature: temperature,
	})
	if err != nil {
		return "", false
	}
	for _, b := range resp.Content {
		if b.Type == providers.BlockText && strings.TrimSpace(b.Text) != "" {
			return b.Text, true
		}
	}
	return "", false
}

// parseJSONArray tolerantly unmarshals a model's JSON array, stripping markdown fences and
// any prose before/after the outermost [ ].
func parseJSONArray(text string, out interface{}) bool {
	t := strings.TrimSpace(text)
	t = strings.TrimPrefix(t, "```json")
	t = strings.TrimPrefix(t, "```")
	t = strings.TrimSuffix(t, "```")
	i, j := strings.Index(t, "["), strings.LastIndex(t, "]")
	if i < 0 || j <= i {
		return false
	}
	return json.Unmarshal([]byte(t[i:j+1]), out) == nil
}
