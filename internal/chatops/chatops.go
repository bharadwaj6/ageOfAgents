package chatops

import (
	"context"
	"fmt"
	"log"

	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// Notifier defines the interface for communicating with ChatOps platforms
// like Slack, MS Teams, or Discord.
type Notifier interface {
	// PromptApproval sends an interactive message asking a human to approve
	// or reject a parked proposal.
	PromptApproval(ctx context.Context, ticketID string, summary string, diff string) error

	// NotifyGoalAmended informs the channel that a goal was dynamically amended.
	NotifyGoalAmended(ctx context.Context, event api.TicketAmendedPayload) error

	// NotifyTicketFailed alerts the channel that a ticket has failed and exhausted retries.
	NotifyTicketFailed(ctx context.Context, ticketID, reason string) error
}

// SlackNotifier is a stub implementation for Slack.
// TODO: Implement actual Slack API integration (issue #78)
type SlackNotifier struct {
	WebhookURL string
}

func (s *SlackNotifier) PromptApproval(ctx context.Context, ticketID string, summary string, diff string) error {
	log.Printf("[Slack Stub] Prompting approval for ticket %s: %s\n", ticketID, summary)
	return nil
}

func (s *SlackNotifier) NotifyGoalAmended(ctx context.Context, event api.TicketAmendedPayload) error {
	log.Printf("[Slack Stub] Goal amended for ticket %s. New title: %s\n", event.TicketID, event.Title)
	return nil
}

func (s *SlackNotifier) NotifyTicketFailed(ctx context.Context, ticketID, reason string) error {
	log.Printf("[Slack Stub] Ticket failed %s: %s\n", ticketID, reason)
	return nil
}

// TeamsNotifier is a stub implementation for Microsoft Teams.
// TODO: Implement actual MS Teams Webhook integration (issue #78)
type TeamsNotifier struct {
	WebhookURL string
}

func (t *TeamsNotifier) PromptApproval(ctx context.Context, ticketID string, summary string, diff string) error {
	fmt.Printf("[Teams Stub] Prompting approval for ticket %s: %s\n", ticketID, summary)
	return nil
}

func (t *TeamsNotifier) NotifyGoalAmended(ctx context.Context, event api.TicketAmendedPayload) error {
	fmt.Printf("[Teams Stub] Goal amended for ticket %s. New title: %s\n", event.TicketID, event.Title)
	return nil
}

func (t *TeamsNotifier) NotifyTicketFailed(ctx context.Context, ticketID, reason string) error {
	fmt.Printf("[Teams Stub] Ticket failed %s: %s\n", ticketID, reason)
	return nil
}
