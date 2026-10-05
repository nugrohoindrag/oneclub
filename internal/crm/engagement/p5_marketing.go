package engagement

// PRD P5 FR-JRN-05: the consent, suppression and contact rules of P3
// campaigns for one customer and channel, exported for the journeys of
// crm/journey (every message step of a journey applies them).

import (
	"context"

	"github.com/google/uuid"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/kernel/errs"
	"oneclub/internal/platform/handle"
)

// MarketingAddressee is a customer as the addressee of one message.
type MarketingAddressee struct {
	CustomerID uuid.UUID
	Name       string
	Address    string // e-mail, E.164 phone or the portal user id (in-app)
	UserID     *uuid.UUID
	Locale     string
	// Status is queued, skipped_no_consent, skipped_suppressed or
	// skipped_no_contact.
	Status string
}

// Addressee classifies a customer for a message on a channel: consent per
// channel (marketing only; transactional messages need no marketing
// consent), the suppression list and a usable contact.
func Addressee(ctx context.Context, q dbtx.Querier, property, customer uuid.UUID, channel string, transactional bool) (MarketingAddressee, error) {
	rows, err := q.Query(ctx, consentSQL+` WHERE c.id = $1 AND c.status = 'active' AND c.erased_at IS NULL`, customer, channel)
	r, err := handle.One[recipientInfo](rows, err, "customer")
	if err != nil {
		if e, ok := errs.As(err); ok && e.Kind == errs.KindNotFound {
			return MarketingAddressee{CustomerID: customer, Status: "skipped_no_contact"}, nil
		}
		return MarketingAddressee{}, err
	}
	sup, err := suppressedSet(ctx, q, property, channel)
	if err != nil {
		return MarketingAddressee{}, err
	}
	if transactional {
		r.Consent = true
	}
	return MarketingAddressee{CustomerID: r.ID, Name: r.Name, Address: r.address(channel), UserID: r.UserID, Locale: r.Locale,
		Status: classify(r, channel, sup)}, nil
}

// Render executes a message template ({{.name}} …) with the recipient data.
func Render(src string, data map[string]any) string { return render(src, data) }

// LinkLanguage is the website language of a locale (id / en).
func LinkLanguage(locale string) string { return linkLang(&locale) }
