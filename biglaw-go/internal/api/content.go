// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Content routes — document library, file upload, tabulate CSV export,
// per-profile cost, and lawyer tone profiles. HTTP contract mirrors the
// TypeScript backend (src/mcp/server.ts).

package api

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/linkedin"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/services"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// contentMaxUploadBytes mirrors the TS backend's @fastify/multipart limit
// (25 MB per file).
const contentMaxUploadBytes = 25 << 20

// contentTextExts are the extensions read as plain text on upload, matching
// the TEXT_EXT list in the TS /documents/upload handler.
var contentTextExts = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".csv": true,
	".json": true, ".log": true, ".text": true, ".rtf": true,
}

// registerContentRoutes adds the document library, upload, CSV export,
// profile cost, and tone profile routes. Param names reuse the existing
// trees (/tasks/:id, /profiles/:id) to satisfy Gin's one-name-per-position
// constraint.
func (s *Server) registerContentRoutes(r *gin.Engine) {
	r.GET("/documents", s.handleListDocuments)
	r.POST("/documents/upload", s.handleUploadDocument)
	r.GET("/tasks/:id/table.csv", s.handleTaskTableCSV)
	r.GET("/profiles/:id/cost", s.handleProfileCost)
	r.POST("/profiles/:id/tone/linkedin-import", s.handleToneLinkedInImport)
	r.DELETE("/profiles/:id/tone", s.handleClearToneProfile)
}

// ─── Document library ─────────────────────────────────────────────────────────

// contentDocSummary is the per-document listing shape returned by
// GET /documents — mirrors KnowledgeStore.listDocuments() in TS (no content,
// no embedding).
type contentDocSummary struct {
	ID                   string `json:"id"`
	Title                string `json:"title"`
	Jurisdiction         string `json:"jurisdiction,omitempty"`
	DocumentType         string `json:"documentType,omitempty"`
	PracticeArea         string `json:"practiceArea,omitempty"`
	DetectedClientNumber string `json:"detectedClientNumber,omitempty"`
	IngestedAt           string `json:"ingestedAt,omitempty"`
}

// handleListDocuments lists the document library. A lawyer sees only
// documents they own; partners see the whole library (same scoping as the
// TS docOwnerScope helper).
func (s *Server) handleListDocuments(c *gin.Context) {
	u := getUser(c)
	partner := auth.IsPartner(u)

	out := []contentDocSummary{}
	for _, d := range s.knowledge.ListAll() {
		if !partner && d.OwnerID != u.ProfileID {
			continue
		}
		title := d.Title
		if title == "" {
			title = "Untitled"
		}
		ingestedAt := ""
		if !d.IngestedAt.IsZero() {
			ingestedAt = d.IngestedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, contentDocSummary{
			ID:                   d.ID,
			Title:                title,
			Jurisdiction:         d.Jurisdiction,
			DocumentType:         d.DocumentType,
			PracticeArea:         d.PracticeArea,
			DetectedClientNumber: d.DetectedClientNumber,
			IngestedAt:           ingestedAt,
		})
	}
	c.JSON(http.StatusOK, out)
}

// ─── Document upload ──────────────────────────────────────────────────────────

// handleUploadDocument accepts a multipart file upload, extracts its text,
// classifies it (practice area + client), and ingests it into the knowledge
// store. Mirrors POST /documents/upload in the TS backend.
//
// PDF gap: the Go port has no PDF text extraction (the TS backend shells out
// to the Python PyMuPDF pipeline, which is absent here), so .pdf uploads
// return 422 instead of being extracted.
func (s *Server) handleUploadDocument(c *gin.Context) {
	u := getUser(c)

	fh := contentFirstFile(c)
	if fh == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No file uploaded"})
		return
	}
	if fh.Size > contentMaxUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "File too large (max 25 MB)"})
		return
	}

	filename := fh.Filename
	if filename == "" {
		filename = "document"
	}
	ext := strings.ToLower(filepath.Ext(filename))
	mimetype := fh.Header.Get("Content-Type")

	var content string
	switch {
	case ext == ".pdf":
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": "PDF extraction not available in the Go backend; upload text",
		})
		return
	case contentTextExts[ext] || strings.HasPrefix(mimetype, "text/"):
		buf, err := contentReadFile(fh)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error": fmt.Sprintf("Could not read %s: %s", filename, err.Error()),
			})
			return
		}
		content = string(buf)
	default:
		typeLabel := ext
		if typeLabel == "" {
			typeLabel = mimetype
		}
		c.JSON(http.StatusUnsupportedMediaType, gin.H{
			"error": fmt.Sprintf("Unsupported file type '%s'. Upload a PDF or text file (or paste the text in the Library).", typeLabel),
		})
		return
	}

	if strings.TrimSpace(content) == "" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": fmt.Sprintf("No extractable text found in %s (a scanned image PDF needs OCR, which isn't wired to upload yet).", filename),
		})
		return
	}

	title := strings.TrimSuffix(filepath.Base(filename), ext)

	// Haiku classification — graceful: on provider failure the classifier
	// returns empty results and the document still ingests.
	practiceArea := ""
	var detectedClient *types.Client
	if clf := s.contentClassifier(); clf != nil {
		practiceArea = clf.DetectPracticeArea(title, content)
		detectedClient = clf.DetectClient(title, content, contentClientPtrs(s.clients.List()))
	}

	doc := types.Document{
		Title:        title,
		Content:      content,
		DocumentType: strings.TrimPrefix(ext, "."),
		Source:       "upload",
		OwnerID:      u.ProfileID,
		PracticeArea: practiceArea,
		IngestedAt:   time.Now(),
	}
	if detectedClient != nil {
		doc.DetectedClientNumber = detectedClient.ClientNumber
	}

	result, err := s.knowledge.Ingest(doc)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "ingest failed: " + err.Error()})
		return
	}

	suggestedLawyers := []gin.H{}
	if practiceArea != "" {
		for _, p := range s.profiles.List() {
			for _, pa := range p.PracticeAreas {
				if pa == practiceArea {
					suggestedLawyers = append(suggestedLawyers, gin.H{
						"id": p.ID, "name": p.Name, "email": p.Email,
					})
					break
				}
			}
		}
	}

	detectedClientNumber := ""
	if detectedClient != nil {
		detectedClientNumber = detectedClient.ClientNumber
	}
	audit.Default.Write(audit.WriteRequest{
		Event:   "document.uploaded",
		ActorID: u.ProfileID,
		Data: map[string]interface{}{
			"docId": result.ID, "title": title, "filename": filename,
			"practiceArea": practiceArea, "detectedClientNumber": detectedClientNumber,
		},
	})

	c.JSON(http.StatusCreated, gin.H{
		"id":               result.ID,
		"title":            title,
		"practiceArea":     contentNullableStr(practiceArea),
		"detectedClient":   detectedClient,
		"suggestedLawyers": suggestedLawyers,
	})
}

// ─── Tabulate CSV export ──────────────────────────────────────────────────────

// handleTaskTableCSV streams the task's tabulate output as a downloadable
// CSV. Access-controlled like the task itself (404, never 403, to avoid
// revealing matter existence).
func (s *Server) handleTaskTableCSV(c *gin.Context) {
	u := getUser(c)
	taskID := c.Param("id")
	task := s.orch.GetTask(taskID)
	if task == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}
	if !auth.CanViewTask(u, task.AssignedLawyerIDs) && task.CreatedByProfileID != u.ProfileID && !auth.IsPartner(u) {
		c.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return
	}
	if task.Table == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "No table available for this task"})
		return
	}

	columns, rows := task.Table.Columns, task.Table.Rows

	hasConf := false
	for _, r := range rows {
		if r["_confidence"] != "" {
			hasConf = true
			break
		}
	}
	outCols := columns
	if hasConf {
		outCols = append(append([]string{}, columns...), "Confidence", "Sources")
	}

	cellFor := func(r map[string]string, col string) string {
		switch col {
		case "Confidence":
			return r["_confidence"]
		case "Sources":
			return r["_sources"]
		default:
			return r[col]
		}
	}

	lines := make([]string, 0, len(rows)+1)
	header := make([]string, len(outCols))
	for i, col := range outCols {
		header[i] = contentCSVEscape(col)
	}
	lines = append(lines, strings.Join(header, ","))
	for _, r := range rows {
		cells := make([]string, len(outCols))
		for i, col := range outCols {
			cells[i] = contentCSVEscape(cellFor(r, col))
		}
		lines = append(lines, strings.Join(cells, ","))
	}

	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "big-michael-"+taskID+".csv"))
	c.Data(http.StatusOK, "text/csv; charset=utf-8", []byte(strings.Join(lines, "\r\n")))
}

// ─── Per-profile cost ─────────────────────────────────────────────────────────

// handleProfileCost returns the cost entries attributed to a profile (tone
// analysis + tasks created by it). Partners see any profile; lawyers only
// their own.
func (s *Server) handleProfileCost(c *gin.Context) {
	u := getUser(c)
	profileID := c.Param("id")
	if !auth.IsPartner(u) && u.ProfileID != profileID {
		c.JSON(http.StatusForbidden, gin.H{"error": "You can only view your own cost data"})
		return
	}
	if s.profiles.Get(profileID) == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "profile not found"})
		return
	}

	entries := s.costs.ForProfile(profileID)
	if entries == nil {
		// Summarise(nil) summarises the entire store — make the empty case
		// explicit so a profile with no entries gets a zero summary.
		entries = []cost.CostEntry{}
	}
	c.JSON(http.StatusOK, gin.H{
		"profileId": profileID,
		"summary":   s.costs.Summarise(entries),
		"entries":   entries,
	})
}

// ─── Tone profiles ────────────────────────────────────────────────────────────

// handleToneLinkedInImport accepts a LinkedIn data-export ZIP or bare CSV,
// extracts the posts, runs the chunked Haiku tone analysis, and stores the
// resulting ToneProfile. Partner or self; 60 s per-profile rate limit.
func (s *Server) handleToneLinkedInImport(c *gin.Context) {
	u := getUser(c)
	profileID := c.Param("id")
	if !auth.IsPartner(u) && u.ProfileID != profileID {
		c.JSON(http.StatusForbidden, gin.H{"error": "You can only import tone for your own profile"})
		return
	}
	profile := s.profiles.Get(profileID)
	if profile == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "profile not found"})
		return
	}

	fh := contentFirstFile(c)
	if fh == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No file uploaded"})
		return
	}
	if fh.Size > contentMaxUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "File too large (max 25 MB)"})
		return
	}
	buf, err := contentReadFile(fh)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File upload failed"})
		return
	}

	// Rate limit: 60 s since the last tone generation for this profile.
	if profile.ToneProfile != nil && profile.ToneProfile.GeneratedAt != "" {
		if generated, perr := time.Parse(time.RFC3339, profile.ToneProfile.GeneratedAt); perr == nil {
			if time.Since(generated) < 60*time.Second {
				c.JSON(http.StatusTooManyRequests, gin.H{"error": "Tone profile was just updated. Please wait before importing again."})
				return
			}
		}
	}

	posts := linkedin.ParseLinkedInExport(buf)
	if len(posts) == 0 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":     "No posts found in export. Upload the ZIP from linkedin.com/mypreferences/d/download-my-data or the extracted Shares.csv / Posts and Articles.csv.",
			"exportUrl": "https://www.linkedin.com/mypreferences/d/download-my-data",
		})
		return
	}

	analyzer := s.contentToneAnalyzer()
	if analyzer == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Tone analysis failed. Please try again."})
		return
	}
	tone, err := analyzer.Analyze(posts, profile.Name, "linkedin_export", profileID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Tone analysis failed. Please try again."})
		return
	}
	updated, err := s.profiles.UpdateTone(profileID, tone)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Tone analysis failed. Please try again."})
		return
	}

	audit.Default.Write(audit.WriteRequest{
		Event:   "profile.tone.imported",
		ActorID: u.ProfileID,
		Data: map[string]interface{}{
			"profileId": profileID, "sampleCount": len(posts), "sourceType": "linkedin_export",
		},
	})

	c.JSON(http.StatusOK, gin.H{
		"toneProfile":     updated.ToneProfile,
		"samplesAnalysed": len(posts),
		"sourceType":      "linkedin_export",
	})
}

// handleClearToneProfile clears a lawyer's tone profile. Partner or self.
// Returns the updated profile (TS contract).
func (s *Server) handleClearToneProfile(c *gin.Context) {
	u := getUser(c)
	profileID := c.Param("id")
	if !auth.IsPartner(u) && u.ProfileID != profileID {
		c.JSON(http.StatusForbidden, gin.H{"error": "You can only clear your own tone profile"})
		return
	}

	updated, err := s.profiles.UpdateTone(profileID, nil)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}

	audit.Default.Write(audit.WriteRequest{
		Event:   "profile.tone.cleared",
		ActorID: u.ProfileID,
		Data:    map[string]interface{}{"profileId": profileID},
	})
	c.JSON(http.StatusOK, updated)
}

// ─── File-local helpers ───────────────────────────────────────────────────────

// contentClassifier builds a Haiku classifier from the provider registry.
// Returns nil only when no provider can serve the Haiku model.
func (s *Server) contentClassifier() *services.Classifier {
	p, err := s.orch.Providers().Get(routing.ModelHaiku)
	if err != nil || p == nil {
		return nil
	}
	return services.New(p, routing.ModelHaiku)
}

// contentToneAnalyzer builds a Haiku tone analyzer from the provider registry.
func (s *Server) contentToneAnalyzer() *services.ToneAnalyzer {
	p, err := s.orch.Providers().Get(routing.ModelHaiku)
	if err != nil || p == nil {
		return nil
	}
	return services.NewToneAnalyzer(p, routing.ModelHaiku)
}

// contentFirstFile returns the first uploaded file in the multipart form,
// regardless of field name — mirrors fastify-multipart's req.file().
func contentFirstFile(c *gin.Context) *multipart.FileHeader {
	form, err := c.MultipartForm()
	if err != nil || form == nil {
		return nil
	}
	for _, fhs := range form.File {
		if len(fhs) > 0 {
			return fhs[0]
		}
	}
	return nil
}

// contentReadFile reads an uploaded file fully, capped at the upload limit.
func contentReadFile(fh *multipart.FileHeader) ([]byte, error) {
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, contentMaxUploadBytes+1))
}

// contentCSVEscape quotes a CSV cell, doubling embedded quotes (RFC 4180).
func contentCSVEscape(v string) string {
	return `"` + strings.ReplaceAll(v, `"`, `""`) + `"`
}

// contentNullableStr returns nil for the empty string so JSON renders null
// (matching the TS practiceArea: null contract).
func contentNullableStr(v string) interface{} {
	if v == "" {
		return nil
	}
	return v
}

// contentClientPtrs adapts ClientStore.List()'s value slice to the pointer
// slice the classifier expects.
func contentClientPtrs(list []types.Client) []*types.Client {
	out := make([]*types.Client, len(list))
	for i := range list {
		out[i] = &list[i]
	}
	return out
}
