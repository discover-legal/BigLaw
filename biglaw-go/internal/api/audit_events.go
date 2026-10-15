// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Discover Legal

package api

import (
	"sort"

	"github.com/gin-gonic/gin"

	"github.com/discover-legal/biglaw-go/internal/audit"
)

// auditAdmin records an administrative change (client roster, matters,
// lawyer profiles, budgets) in the hash-chained audit log, attributed to the
// request's session user. Every roster/profile mutation handler calls it
// after the change succeeds, so the audit trail shows who changed what.
func auditAdmin(c *gin.Context, event string, data map[string]interface{}) {
	actor := audit.ActorAnonymous
	if u := getUser(c); u != nil && u.ProfileID != "" {
		actor = u.ProfileID
	}
	if data == nil {
		data = map[string]interface{}{}
	}
	audit.Default.Write(audit.WriteRequest{Event: event, ActorID: actor, Data: data})
}

// patchKeys lists the field names a PATCH body touched (values are not
// logged — they can carry bios, notes and other free text).
func patchKeys(patch map[string]interface{}) []string {
	keys := make([]string, 0, len(patch))
	for k := range patch {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
