// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"net/http"

	"github.com/discover-legal/biglaw-go/internal/auth"
	"github.com/discover-legal/biglaw-go/internal/types"
	"github.com/gin-gonic/gin"
)

// ─── Profiles ─────────────────────────────────────────────────────────────────

func (s *Server) handleListProfiles(c *gin.Context) {
	list := s.profiles.List()
	if list == nil {
		list = []types.LawyerProfile{}
	}
	c.JSON(http.StatusOK, list)
}

func (s *Server) handleGetProfile(c *gin.Context) {
	p := s.profiles.Get(c.Param("id"))
	if p == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "profile not found"})
		return
	}
	c.JSON(http.StatusOK, p)
}

type createProfileBody struct {
	Name          string   `json:"name"`
	Email         string   `json:"email"`
	Role          string   `json:"role"`
	Title         string   `json:"title"`
	Color         string   `json:"color"`
	PracticeAreas []string `json:"practiceAreas"`
	Bio           string   `json:"bio"`
	Mode          string   `json:"mode"`
}

func (s *Server) handleCreateProfile(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	var body createProfileBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}

	p, err := s.profiles.Create(auth.CreateProfileInput{
		Name:          body.Name,
		Email:         body.Email,
		Role:          body.Role,
		Title:         body.Title,
		Color:         body.Color,
		PracticeAreas: body.PracticeAreas,
		Bio:           body.Bio,
		Mode:          body.Mode,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, p)
}

func (s *Server) handleUpdateProfile(c *gin.Context) {
	u := getUser(c)
	targetID := c.Param("id")

	// Partners may update anyone; lawyers may only update their own profile
	// but cannot change their role.
	if !auth.IsPartner(u) && u.ProfileID != targetID {
		c.JSON(http.StatusForbidden, gin.H{"error": "cannot update another profile"})
		return
	}

	var patch map[string]interface{}
	if err := c.ShouldBindJSON(&patch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
		return
	}

	// Non-partners cannot change their own role.
	if !auth.IsPartner(u) {
		delete(patch, "role")
	}

	p, err := s.profiles.Update(targetID, patch)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, p)
}

func (s *Server) handleDeleteProfile(c *gin.Context) {
	if !requirePartner(c) {
		return
	}
	deleted, err := s.profiles.Remove(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !deleted {
		c.JSON(http.StatusNotFound, gin.H{"error": "profile not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
