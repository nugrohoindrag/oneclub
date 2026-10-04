package notification

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/integration"
	"oneclub/internal/platform/jobs"
	"oneclub/internal/platform/outbox"
)

// DeliverArgs delivers one e-mail or WhatsApp notification.
type DeliverArgs struct {
	DeliveryID uuid.UUID `json:"deliveryId"`
}

func (DeliverArgs) Kind() string { return "notification_deliver" }

func (DeliverArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: jobs.QueueNotifications, MaxAttempts: 5}
}

// Worker sends deliveries via the integration layer. Failures are retried
// with exponential backoff (FR-NOT-03); after the last attempt the delivery
// becomes Failed and the job is discarded, so it appears in Background Jobs
// for Retry/Discard (EP-05 AC).
type Worker struct {
	river.WorkerDefaults[DeliverArgs]
	Svc       *Service
	RetryBase time.Duration // default 10s; 2^(attempt-1) * base, capped at 1h
}

// NextRetry implements exponential backoff.
func (w *Worker) NextRetry(job *river.Job[DeliverArgs]) time.Time {
	base := w.RetryBase
	if base <= 0 {
		base = 10 * time.Second
	}
	d := time.Duration(float64(base) * math.Pow(2, float64(job.Attempt-1)))
	if d > time.Hour {
		d = time.Hour
	}
	return time.Now().Add(d)
}

// Work sends one delivery.
func (w *Worker) Work(ctx context.Context, job *river.Job[DeliverArgs]) error {
	s := w.Svc
	ctx = dbtx.System(ctx)
	var (
		channel, recipient, subject, body, status, event, locale string
		userName                                                 *string
		payload                                                  map[string]any
	)
	err := s.DB.Primary.QueryRow(ctx, `SELECT d.channel, d.recipient, d.subject, d.body, d.status, u.full_name, d.event_code, d.locale, d.payload
		FROM platform.notification_deliveries d LEFT JOIN platform.users u ON u.id = d.user_id WHERE d.id = $1`, job.Args.DeliveryID).
		Scan(&channel, &recipient, &subject, &body, &status, &userName, &event, &locale, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return river.JobCancel(errors.New("delivery not found"))
	}
	if err != nil {
		return err
	}
	if status == "sent" {
		return nil // idempotent on retry after a crash post-send
	}

	var sendErr error
	switch channel {
	case "email":
		mailer, err := s.Integrations.Mailer(ctx)
		if err != nil {
			sendErr = err
			break
		}
		b := s.branding(ctx)
		name := ""
		if userName != nil {
			name = *userName
		}
		sendErr = mailer.SendEmail(ctx, integration.Email{
			To: recipient, ToName: name, FromName: b.EmailSenderName, Subject: subject, Text: body, HTML: EmailHTML(b, subject, body),
		})
	case "whatsapp":
		m, err := s.Integrations.Messaging(ctx)
		if errors.Is(err, integration.ErrNotConfigured) {
			_, uerr := s.DB.Primary.Exec(ctx, `UPDATE platform.notification_deliveries SET status = 'skipped', attempts = attempts + 1,
				last_error = 'WhatsApp is not configured (BSP pending)' WHERE id = $1`, job.Args.DeliveryID)
			return uerr
		}
		if err != nil {
			sendErr = err
			break
		}
		params := map[string]string{}
		for k, v := range payload {
			params[k] = fmt.Sprint(v)
		}
		var res integration.MessageResult
		res, sendErr = m.SendMessage(ctx, integration.OutboundMessage{To: recipient, Template: event, Language: locale,
			Named: params, Text: subject + "\n\n" + body})
		if sendErr == nil && res.ExternalID != "" {
			_, _ = s.DB.Primary.Exec(ctx, `UPDATE platform.notification_deliveries SET external_id = $2, delivery_status = $3 WHERE id = $1`,
				job.Args.DeliveryID, res.ExternalID, res.Status)
		}
	default:
		return river.JobCancel(errors.New("unsupported channel " + channel))
	}

	if sendErr != nil {
		final := job.Attempt >= job.MaxAttempts
		newStatus := "pending"
		if final {
			newStatus = "failed"
		}
		_, _ = s.DB.Primary.Exec(ctx, `UPDATE platform.notification_deliveries SET status = $2, attempts = $3, last_error = $4,
			failed_at = CASE WHEN $2 = 'failed' THEN now() ELSE failed_at END WHERE id = $1`,
			job.Args.DeliveryID, newStatus, job.Attempt, sendErr.Error())
		return sendErr
	}
	_, err = s.DB.Primary.Exec(ctx, `UPDATE platform.notification_deliveries SET status = 'sent', attempts = $2, sent_at = now(), last_error = NULL WHERE id = $1`,
		job.Args.DeliveryID, job.Attempt)
	return err
}

// OnDeliveryStatus records WhatsApp delivery statuses reported by the BSP
// webhook (sent → delivered → read / failed).
func (s *Service) OnDeliveryStatus(ctx context.Context, tx pgx.Tx, e outbox.Event) error {
	var p struct {
		Capability string `json:"capability"`
		Data       struct {
			Statuses []struct {
				MessageID string `json:"messageId"`
				Status    string `json:"status"`
			} `json:"statuses"`
		} `json:"data"`
	}
	if err := e.Decode(&p); err != nil {
		return err
	}
	if p.Capability != integration.CapMessaging {
		return nil
	}
	for _, st := range p.Data.Statuses {
		if _, err := tx.Exec(ctx, `UPDATE platform.notification_deliveries SET delivery_status = $2,
			delivered_at = CASE WHEN $2 IN ('delivered', 'read') THEN coalesce(delivered_at, now()) ELSE delivered_at END,
			read_at = CASE WHEN $2 = 'read' THEN now() ELSE read_at END,
			status = CASE WHEN $2 = 'failed' THEN 'failed' ELSE status END
			WHERE external_id = $1`, st.MessageID, st.Status); err != nil {
			return err
		}
	}
	return nil
}
