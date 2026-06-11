// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Twenty CRM integration — GraphQL client.
// Config: TWENTY_API_URL + TWENTY_API_KEY.
// API URL is SSRF-validated (http/https only); responses capped at 2 MB;
// requests time out at 30 s; API key never logged.

package integrations

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

const twentyResponseCap = 2_000_000

// TwentyCompany represents a Twenty CRM Company record.
type TwentyCompany struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	DomainName struct {
		PrimaryLinkURL string `json:"primaryLinkUrl"`
	} `json:"domainName"`
	Employees int `json:"employees"`
	Address   struct {
		City    string `json:"addressCity"`
		Country string `json:"addressCountry"`
	} `json:"address"`
}

// TwentyPerson represents a Twenty CRM Person record.
type TwentyPerson struct {
	ID   string `json:"id"`
	Name struct {
		FirstName string `json:"firstName"`
		LastName  string `json:"lastName"`
	} `json:"name"`
	PrimaryEmail struct {
		Email string `json:"primaryEmail"`
	} `json:"primaryEmail"`
	Company *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"company"`
}

// TwentyNote represents a Twenty CRM Note record.
type TwentyNote struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
}

// TwentyClient wraps the Twenty CRM GraphQL API.
type TwentyClient struct {
	base string
}

// DefaultTwentyClient is the package-level singleton.
var DefaultTwentyClient *TwentyClient

func init() {
	raw := os.Getenv("TWENTY_API_URL")
	if raw == "" {
		DefaultTwentyClient = &TwentyClient{}
		return
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		DefaultTwentyClient = &TwentyClient{}
		return
	}
	base := raw
	if len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	DefaultTwentyClient = &TwentyClient{base: base}
}

// IsConfigured returns true when both URL and API key are set.
func (c *TwentyClient) IsConfigured() bool {
	return c.base != "" && os.Getenv("TWENTY_API_KEY") != ""
}

func (c *TwentyClient) gql(query string, variables map[string]interface{}, out interface{}) error {
	if !c.IsConfigured() {
		return fmt.Errorf("Twenty not configured — set TWENTY_API_URL and TWENTY_API_KEY")
	}
	body := map[string]interface{}{"query": query}
	if variables != nil {
		body["variables"] = variables
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("POST", c.base+"/api", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+os.Getenv("TWENTY_API_KEY"))
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("Twenty API HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, twentyResponseCap))
	if err != nil {
		return err
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("Twenty GraphQL: %s", envelope.Errors[0].Message)
	}
	return json.Unmarshal(envelope.Data, out)
}

// SearchCompanies searches for companies by name.
func (c *TwentyClient) SearchCompanies(query string, limit int) ([]TwentyCompany, error) {
	var result struct {
		Companies struct {
			Edges []struct {
				Node TwentyCompany `json:"node"`
			} `json:"edges"`
		} `json:"companies"`
	}
	q := `query SearchCompanies($filter: CompanyFilterInput, $first: Int) {
		companies(filter: $filter, first: $first, orderBy: {name: AscNullsLast}) {
			edges { node { id name domainName { primaryLinkUrl } employees } }
		}
	}`
	vars := map[string]interface{}{
		"filter": map[string]interface{}{"name": map[string]string{"like": "%" + query + "%"}},
		"first":  limit,
	}
	if err := c.gql(q, vars, &result); err != nil {
		return nil, err
	}
	companies := make([]TwentyCompany, 0, len(result.Companies.Edges))
	for _, e := range result.Companies.Edges {
		companies = append(companies, e.Node)
	}
	return companies, nil
}

// CreateCompany creates a new company.
func (c *TwentyClient) CreateCompany(name, domainName string) (*TwentyCompany, error) {
	var result struct {
		CreateCompany TwentyCompany `json:"createCompany"`
	}
	data := map[string]interface{}{"name": name}
	if domainName != "" {
		data["domainName"] = map[string]string{"primaryLinkUrl": domainName}
	}
	q := `mutation CreateCompany($data: CompanyCreateInput!) {
		createCompany(data: $data) { id name }
	}`
	if err := c.gql(q, map[string]interface{}{"data": data}, &result); err != nil {
		return nil, err
	}
	return &result.CreateCompany, nil
}

// UpsertClientAsCompany finds or creates a company matching the client name.
func (c *TwentyClient) UpsertClientAsCompany(clientName string) (*TwentyCompany, error) {
	results, err := c.SearchCompanies(clientName, 5)
	if err != nil {
		return nil, err
	}
	for i := range results {
		if results[i].Name == clientName {
			return &results[i], nil
		}
	}
	return c.CreateCompany(clientName, "")
}

// CreateNote creates a note attached to a company or person.
func (c *TwentyClient) CreateNote(title, body, companyID, personID string) (*TwentyNote, error) {
	var result struct {
		CreateNote TwentyNote `json:"createNote"`
	}
	data := map[string]interface{}{
		"title": truncate(title, 200),
		"body":  truncate(body, 50_000),
	}
	targets := []map[string]string{}
	if companyID != "" {
		targets = append(targets, map[string]string{"companyId": companyID})
	}
	if personID != "" {
		targets = append(targets, map[string]string{"personId": personID})
	}
	if len(targets) > 0 {
		data["noteTargets"] = map[string]interface{}{
			"createMany": map[string]interface{}{"data": targets},
		}
	}
	q := `mutation CreateNote($data: NoteCreateInput!) {
		createNote(data: $data) { id title createdAt }
	}`
	if err := c.gql(q, map[string]interface{}{"data": data}, &result); err != nil {
		return nil, err
	}
	return &result.CreateNote, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
