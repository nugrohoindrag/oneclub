package notification

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"oneclub/internal/kernel/dbtx"
	"oneclub/internal/platform/integration"
	"oneclub/internal/platform/jobs"
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
		channel, recipient, subject, body, status string
		userName                                  *string
	)
	err := s.DB.Primary.QueryRow(ctx, `SELECT d.channel, d.recipient, d.subject, d.body, d.status, u.full_name
		FROM platform.notification_deliveries d LEFT JOIN platform.users u ON u.id = d.user_id WHERE d.id = $1`, job.Args.DeliveryID).
		Scan(&channel, &recipient, &subject, &body, &status, &userName)
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
		_, sendErr = m.SendMessage(ctx, integration.OutboundMessage{To: recipient, Text: subject + "\n\n" + body})
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
