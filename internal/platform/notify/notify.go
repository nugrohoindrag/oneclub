// Package notify defines the public notification contract used by every
// module (EP-05). The notification package implements Sender; modules only
// depend on this small package.
package notify

import (
	"context"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5"
)

// Channels.
const (
	ChannelInApp    = "in_app"
	ChannelEmail    = "email"
	ChannelWhatsApp = "whatsapp"
)

// Message is a notification request. Content comes from the template for
// (Event, channel, recipient locale); Data fills template variables.
type Message struct {
	Event      string         // e.g. "approval.pending"
	Category   string         // preference category, e.g. "approval"
	UserIDs    []uuid.UUID    // recipients
	Email      string         // direct recipient without a user account (guests, non-members)
	Phone      string         // direct WhatsApp recipient (E.164) without a user account
	Name       string         // display name of the direct recipient
	Locale     string         // for direct recipients
	Data       map[string]any // template variables
	Link       string         // deep link for in-app notifications
	Channels   []string       // default: in_app + email
	Mandatory  bool           // cannot be opted out (security, approvals)
	PropertyID *uuid.UUID
}

// Sender queues notifications inside the caller's transaction, so they are
// only sent if the business change commits.
type Sender interface {
	Send(ctx context.Context, tx pgx.Tx, m Message) error
}
