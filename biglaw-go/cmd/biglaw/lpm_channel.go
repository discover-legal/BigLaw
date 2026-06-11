// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (C) 2026 Discover Legal

// Channel wiring for LPM — connects the outbound drafter's "channel" write-mode
// and the daily-report notifier to Big Michael's existing Teams/Slack senders.
// Matter→channel resolution and credentials come from the bot env config
// (TEAMS_MATTER_WEBHOOKS / TEAMS_INCOMING_WEBHOOK_URL, SLACK_MATTER_CHANNELS /
// SLACK_DEFAULT_CHANNEL), so this works without mounting the inbound bot routes.
package main

import (
	"fmt"
	"os"

	"github.com/discover-legal/biglaw-go/internal/bots"
	"github.com/discover-legal/biglaw-go/internal/config"
	"github.com/discover-legal/biglaw-go/internal/lpm"
)

// newMatterChannelPoster returns a ChannelPoster that posts to a matter's linked
// Teams and/or Slack channel. Returns nil when no chat platform is configured, so
// the drafter cleanly reports that "channel" mode is unavailable rather than
// silently dropping messages.
func newMatterChannelPoster(cfg *config.Config) lpm.ChannelPoster {
	teamsOn := cfg.Bots.Teams.Enabled
	slackOn := cfg.Bots.Slack.Enabled
	if !teamsOn && !slackOn {
		return nil
	}
	return func(d lpm.Draft) error {
		posted := false
		var firstErr error

		if teamsOn {
			if err := bots.PostToMatter(d.MatterNumber, d.Subject, d.Body); err != nil {
				firstErr = err
			} else {
				posted = true
			}
		}
		if slackOn {
			channel := ""
			if l, ok := bots.GetSlackMatterLink(d.MatterNumber); ok {
				channel = l.ChannelID
			}
			if channel == "" {
				channel = os.Getenv("SLACK_DEFAULT_CHANNEL")
			}
			if channel != "" {
				text := d.Subject + "\n\n" + d.Body
				if err := bots.PostToSlackChannel(channel, text); err != nil {
					if firstErr == nil {
						firstErr = err
					}
				} else {
					posted = true
				}
			}
		}

		if posted {
			return nil
		}
		if firstErr != nil {
			return firstErr
		}
		return fmt.Errorf("no channel linked to matter %s", d.MatterNumber)
	}
}
