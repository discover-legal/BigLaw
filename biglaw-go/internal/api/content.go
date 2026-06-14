// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Content routes — document library, file upload, tabulate CSV export,
// per-profile cost, and lawyer tone profiles. HTTP contract mirrors the
// TypeScript backend (src/mcp/server.ts).

package api

import (
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/discover-legal/biglaw-go/internal/audit"
	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/cost"
	"github.com/discover-legal/biglaw-go/internal/csvutil"
	"github.com/discover-legal/biglaw-go/internal/linkedin"
	"github.com/discover-legal/biglaw-go/internal/pdfgen"
	"github.com/discover-legal/biglaw-go/internal/routing"
	"github.com/discover-legal/biglaw-go/internal/services"
	"github.com/discover-legal/biglaw-go/internal/types"
)

// contentMaxUploadBytes mirrors the TS backend's @fastify/multipart limit
// (25 MB per file).
const contentMaxUploadBytes = 25 << 20

// registerContentRoutes adds the document library, upload, CSV export,
// profile cost, and tone profile routes. Param names reuse the existing
// trees (/tasks/:id, /profiles/:id) to satisfy Gin's one-name-per-position
// constraint.
func (s *Server) registerContentRoutes(r *gin.Engine) {
	r.GET("/documents", s.handleListDocuments)
	r.POST("/documents/upload", s.handleUploadDocument)
	r.GET("/documents/attachments/:docId", s.handleListAttachments)
	r.GET("/documents/attachments/:docId/:attId", s.handleGetAttachment)
	r.GET("/documents/export/:docId", s.handleExportDocument)
	r.GET("/tasks/:id/table.csv", s.handleTaskTableCSV)
	r.GET("/profiles/:id/cost", s.handleProfileCost)
	r.POST("/profiles/:id/tone/import", s.handleToneImport)
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

	// Source from the durable repository under the caller's identity so the
	// database RLS layer applies (default-deny on Postgres); the app-layer
	// owner filter below is the second layer and the sole filter on SQLite.
	docs, err := s.knowledge.ListVisible(reqIdentity(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list failed: " + err.Error()})
		return
	}

	out := []contentDocSummary{}
	for _, d := range docs {
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

// handleUploadDocument accepts a multipart file upload, extracts its text via
// the hybrid omnimodal pipeline (text layer as ground truth + Qwen-VL vision
// reconcile), classifies it (practice area + client), and ingests it into the
// knowledge store. Accepts PDF (digital + scanned), Word (.docx), images, and
// plain text; only a genuinely unreadable file returns 422, with a specific
// reason in the body.
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

	buf, err := contentReadFile(fh)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error": fmt.Sprintf("Could not read %s: %s", filename, err.Error()),
		})
		return
	}

	// Hybrid omnimodal extraction: text layer as ground truth, vision model
	// (Qwen-VL) reconciles scans/tables/images. PDFs, Word docs, images, and
	// plain text are all accepted — no more blanket PDF/415 rejection.
	res := s.contentExtractor().Extract(filename, buf)
	content := res.Text
	if strings.TrimSpace(content) == "" {
		// No text — but if it's a visual file we can retain (image/PDF) and a
		// blob store is available, keep it as an image-only document rather than
		// rejecting it. This is the "place images / mainly text but not only"
		// path: an uploaded exhibit/photo lives on even with no vision model.
		if contentRetainOriginal(ext, fh.Header.Get("Content-Type")) && s.blobs != nil {
			content = fmt.Sprintf("[Image document: %s]", filename)
		} else {
			msg := fmt.Sprintf("No extractable text found in %s.", filename)
			if len(res.Notes) > 0 {
				msg = strings.Join(res.Notes, " ")
			}
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error":            msg,
				"extractionMethod": res.Method,
			})
			return
		}
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

	result, err := s.knowledge.Ingest(reqIdentity(c), doc)
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

	// Retain the original bytes of visual uploads (images, PDFs) as an
	// attachment so the source can be viewed and placed into outputs later —
	// not just its transcribed text. Bytes go to the blob store; metadata to
	// the RLS-scoped repository under the uploader's identity.
	attachments := []types.Attachment{}
	if s.blobs != nil && contentRetainOriginal(ext, fh.Header.Get("Content-Type")) {
		attID := uuid.New().String()
		key := result.ID + "/" + attID
		if perr := s.blobs.Put(key, buf); perr != nil {
			slog.Warn("attachment blob write failed", "docId", result.ID, "err", perr)
		} else {
			att := types.Attachment{
				ID: attID, DocID: result.ID, OwnerID: u.ProfileID,
				Filename: filename, MediaType: contentUploadMIME(ext, fh.Header.Get("Content-Type")),
				Kind: types.AttachmentOriginal, Size: int64(len(buf)), BlobKey: key,
				CreatedAt: time.Now(),
			}
			if aerr := s.knowledge.AddAttachment(reqIdentity(c), att); aerr != nil {
				slog.Warn("attachment metadata write failed", "docId", result.ID, "err", aerr)
				_ = s.blobs.Delete(key) // don't orphan bytes with no metadata
			} else {
				attachments = append(attachments, att)
			}
		}
	}

	audit.Default.Write(audit.WriteRequest{
		Event:   "document.uploaded",
		ActorID: u.ProfileID,
		Data: map[string]interface{}{
			"docId": result.ID, "title": title, "filename": filename,
			"practiceArea": practiceArea, "detectedClientNumber": detectedClientNumber,
			"attachments": len(attachments),
		},
	})

	c.JSON(http.StatusCreated, gin.H{
		"id":               result.ID,
		"title":            title,
		"practiceArea":     contentNullableStr(practiceArea),
		"detectedClient":   detectedClient,
		"suggestedLawyers": suggestedLawyers,
		"extractionMethod": res.Method,
		"extractionNotes":  res.Notes,
		"attachments":      attachments,
	})
}

// ─── Attachments ───────────────────────────────────────────────────────────────

// handleListAttachments lists a document's attachments, RLS-scoped to the
// caller (default-deny on Postgres; app-layer elsewhere).
func (s *Server) handleListAttachments(c *gin.Context) {
	atts, err := s.knowledge.ListAttachments(reqIdentity(c), c.Param("docId"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if atts == nil {
		atts = []types.Attachment{}
	}
	c.JSON(http.StatusOK, atts)
}

// handleGetAttachment streams an attachment's bytes. Visibility is enforced by
// resolving the metadata through the RLS-scoped repository first; only on a hit
// are the bytes fetched from the blob store. 404 (never 403) so a hidden
// attachment is indistinguishable from a missing one.
func (s *Server) handleGetAttachment(c *gin.Context) {
	att, found, err := s.knowledge.GetAttachment(reqIdentity(c), c.Param("attId"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !found || att.DocID != c.Param("docId") {
		c.JSON(http.StatusNotFound, gin.H{"error": "attachment not found"})
		return
	}
	if s.blobs == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "attachment storage unavailable"})
		return
	}
	data, berr := s.blobs.Get(att.BlobKey)
	if berr != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "attachment bytes not found"})
		return
	}
	mt := att.MediaType
	if mt == "" {
		mt = "application/octet-stream"
	}
	c.Header("Content-Disposition", fmt.Sprintf("inline; filename=%q", att.Filename))
	c.Data(http.StatusOK, mt, data)
}

// handleExportDocument renders a document — its text plus its image
// attachments, placed inline — to a downloadable PDF using the pure-Go
// generator. RLS-scoped: the document and its attachments are resolved under
// the caller's identity.
func (s *Server) handleExportDocument(c *gin.Context) {
	ctx := reqIdentity(c)
	doc, found, err := s.knowledge.GetVisible(ctx, c.Param("docId"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !found || doc == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "document not found"})
		return
	}

	var images []pdfgen.Image
	if s.blobs != nil {
		atts, _ := s.knowledge.ListAttachments(ctx, doc.ID)
		for _, a := range atts {
			if !strings.HasPrefix(a.MediaType, "image/") {
				continue // only images are placed; a PDF original isn't rasterized here
			}
			data, berr := s.blobs.Get(a.BlobKey)
			if berr != nil {
				continue
			}
			images = append(images, pdfgen.Image{MediaType: a.MediaType, Data: data, Caption: a.Filename})
		}
	}

	out, gerr := pdfgen.Generate(doc.Title, doc.Content, images)
	if gerr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "pdf generation failed: " + gerr.Error()})
		return
	}
	name := doc.Title
	if name == "" {
		name = "document"
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+".pdf"))
	c.Data(http.StatusOK, "application/pdf", out)
}

// contentRetainOriginal reports whether an upload's original bytes are worth
// keeping as an attachment — visual formats (images, PDFs) whose source matters
// beyond the extracted text.
func contentRetainOriginal(ext, mimetype string) bool {
	switch ext {
	case ".pdf", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tif", ".tiff":
		return true
	}
	return strings.HasPrefix(mimetype, "image/") || mimetype == "application/pdf"
}

// contentUploadMIME resolves an upload's media type from its extension, falling
// back to the request Content-Type then a generic binary type.
func contentUploadMIME(ext, headerType string) string {
	byExt := map[string]string{
		".pdf": "application/pdf", ".png": "image/png", ".jpg": "image/jpeg",
		".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
		".bmp": "image/bmp", ".tif": "image/tiff", ".tiff": "image/tiff",
	}
	if mt := byExt[ext]; mt != "" {
		return mt
	}
	if headerType != "" {
		return headerType
	}
	return "application/octet-stream"
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

// handleToneImport accepts any writing-sample source — LinkedIn export
// ZIP/CSV, DOCX, PDF, generic CSV, or plain text/Markdown — extracts the
// samples, runs the chunked Haiku tone analysis, and stores the resulting
// ToneProfile. Partner or self; 60 s per-profile rate limit. Mirrors
// POST /profiles/:id/tone/import in the TS backend.
func (s *Server) handleToneImport(c *gin.Context) {
	s.contentToneImport(c, false)
}

// handleToneLinkedInImport accepts a LinkedIn data-export ZIP or bare CSV
// only — the backwards-compatible alias for handleToneImport.
func (s *Server) handleToneLinkedInImport(c *gin.Context) {
	s.contentToneImport(c, true)
}

// contentToneImport is the shared tone-import handler. linkedInOnly selects
// the legacy /tone/linkedin-import contract (LinkedIn parser only, LinkedIn
// 422 message); otherwise samples come from services.ExtractWritingSamples.
func (s *Server) contentToneImport(c *gin.Context, linkedInOnly bool) {
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

	var samples []string
	sourceType := "linkedin_export"
	if linkedInOnly {
		samples = linkedin.ParseLinkedInExport(buf)
		if len(samples) == 0 {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error":     "No posts found in export. Upload the ZIP from linkedin.com/mypreferences/d/download-my-data or the extracted Shares.csv / Posts and Articles.csv.",
				"exportUrl": "https://www.linkedin.com/mypreferences/d/download-my-data",
			})
			return
		}
	} else {
		filename := fh.Filename
		if filename == "" {
			filename = "upload"
		}
		samples, sourceType = services.ExtractWritingSamplesWithSource(filename, buf)
		if len(samples) == 0 {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error":             "No writing samples found in the uploaded file. Accepted formats: LinkedIn export ZIP/CSV, Word (.docx), PDF, plain text, or any CSV with a text column.",
				"linkedInExportUrl": "https://www.linkedin.com/mypreferences/d/download-my-data",
			})
			return
		}
	}

	analyzer := s.contentToneAnalyzer()
	if analyzer == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Tone analysis failed. Please try again."})
		return
	}
	tone, err := analyzer.Analyze(samples, profile.Name, sourceType, profileID)
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
			"profileId": profileID, "sampleCount": len(samples), "sourceType": sourceType,
		},
	})

	c.JSON(http.StatusOK, gin.H{
		"toneProfile":     updated.ToneProfile,
		"samplesAnalysed": len(samples),
		"sourceType":      sourceType,
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
	model := routing.Light(s.cfg)
	p, err := s.orch.Providers().Get(model)
	if err != nil || p == nil {
		return nil
	}
	return services.New(p, model)
}

// contentToneAnalyzer builds a Haiku tone analyzer from the provider registry.
func (s *Server) contentToneAnalyzer() *services.ToneAnalyzer {
	model := routing.Light(s.cfg)
	p, err := s.orch.Providers().Get(model)
	if err != nil || p == nil {
		return nil
	}
	return services.NewToneAnalyzer(p, model)
}

// contentExtractor builds the hybrid document extractor: the Mid-tier model
// reconciles, the Vision-tier model (Qwen-VL by default) reads images and
// scanned pages, and the bundled PyMuPDF script rasterizes PDF pages when
// present. Providers that can't be resolved are passed as nil and the extractor
// degrades gracefully (text-layer only, with a note).
func (s *Server) contentExtractor() *services.DocumentExtractor {
	textModel := routing.Mid(s.cfg)
	textProvider, err := s.orch.Providers().Get(textModel)
	if err != nil {
		textProvider = nil
	}
	visionModel := routing.Vision(s.cfg)
	visionProvider, verr := s.orch.Providers().Get(visionModel)
	if verr != nil {
		visionProvider = nil
	}
	script := os.Getenv("PDF_TOOLS_SCRIPT")
	if script == "" {
		script = filepath.Join("scripts", "pdf_tools.py")
	}
	return services.NewDocumentExtractor(textProvider, textModel, visionProvider, visionModel, s.cfg.PDF.PythonBin, script)
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

// contentCSVEscape quotes a CSV cell, doubling embedded quotes (RFC 4180)
// and neutralizing spreadsheet formula injection (shared helper) — tabulate
// cells carry LLM-generated content.
func contentCSVEscape(v string) string {
	return csvutil.Escape(v)
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
