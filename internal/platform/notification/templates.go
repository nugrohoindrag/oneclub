package notification

import "oneclub/internal/platform/provision"

type tpl struct{ subject, body string }

// defaults are the seeded templates (FR-NOT-02): event → locale → content.
// The same text is used for e-mail and in-app; clubs edit them in
// Settings → Notifications.
var defaults = map[string]map[string]tpl{
	"auth.password_reset": {
		"en": {"Reset your password", "Hello {{.name}},\n\nWe received a request to reset your password. Open the link below within {{.expiresInMinutes}} minutes:\n\n{{.link}}\n\nIf you did not request this, you can ignore this e-mail."},
		"id": {"Atur ulang kata sandi", "Halo {{.name}},\n\nKami menerima permintaan untuk mengatur ulang kata sandi Anda. Buka tautan berikut dalam {{.expiresInMinutes}} menit:\n\n{{.link}}\n\nJika Anda tidak memintanya, abaikan e-mail ini."},
	},
	"auth.account_invite": {
		"en": {"Your OneClub account", "Hello {{.name}},\n\nAn account has been created for you. Set your password with the link below (valid for {{.expiresInMinutes}} minutes):\n\n{{.link}}"},
		"id": {"Akun OneClub Anda", "Halo {{.name}},\n\nAkun Anda telah dibuat. Atur kata sandi melalui tautan berikut (berlaku {{.expiresInMinutes}} menit):\n\n{{.link}}"},
	},
	"approval.pending": {
		"en": {"Approval needed: {{.title}}", "{{.requesterName}} submitted {{.documentType}} \"{{.title}}\" ({{.documentRef}}) for your approval at step {{.stepName}}.\n\nReview it in Approvals: {{.link}}"},
		"id": {"Perlu persetujuan: {{.title}}", "{{.requesterName}} mengajukan {{.documentType}} \"{{.title}}\" ({{.documentRef}}) untuk Anda setujui pada tahap {{.stepName}}.\n\nTinjau di Approvals: {{.link}}"},
	},
	"approval.decided": {
		"en": {"{{.documentType}} {{.decision}}: {{.title}}", "Your request \"{{.title}}\" ({{.documentRef}}) was {{.decision}} by {{.deciderName}}.{{if .reason}}\n\nReason: {{.reason}}{{end}}\n\n{{.link}}"},
		"id": {"{{.documentType}} {{.decision}}: {{.title}}", "Pengajuan \"{{.title}}\" ({{.documentRef}}) telah {{.decision}} oleh {{.deciderName}}.{{if .reason}}\n\nAlasan: {{.reason}}{{end}}\n\n{{.link}}"},
	},
	"approval.reminder": {
		"en": {"Reminder: approval overdue — {{.title}}", "\"{{.title}}\" ({{.documentRef}}) has been waiting for your approval since {{.since}} and is past its SLA.\n\n{{.link}}"},
		"id": {"Pengingat: persetujuan terlambat — {{.title}}", "\"{{.title}}\" ({{.documentRef}}) menunggu persetujuan Anda sejak {{.since}} dan telah melewati SLA.\n\n{{.link}}"},
	},
	"report.export_ready": {
		"en": {"Your export is ready: {{.reportName}}", "The {{.format}} export of {{.reportName}} ({{.rowCount}} rows) is ready.\n\n{{.link}}"},
		"id": {"Ekspor siap: {{.reportName}}", "Ekspor {{.format}} untuk {{.reportName}} ({{.rowCount}} baris) sudah siap.\n\n{{.link}}"},
	},
	"report.export_failed": {
		"en": {"Export failed: {{.reportName}}", "The export of {{.reportName}} failed: {{.error}}"},
		"id": {"Ekspor gagal: {{.reportName}}", "Ekspor {{.reportName}} gagal: {{.error}}"},
	},
	"system.job_alert": {
		"en": {"Background job alert: {{.summary}}", "{{.details}}\n\nOpen Settings → System Settings → Background Jobs to retry or discard: {{.link}}"},
		"id": {"Peringatan proses latar: {{.summary}}", "{{.details}}\n\nBuka Settings → System Settings → Background Jobs untuk Retry atau Discard: {{.link}}"},
	},
	"system.test": {
		"en": {"Test notification", "Hello {{.recipientName}},\n\nThis is a test notification sent by {{.senderName}} at {{.sentAt}}."},
		"id": {"Notifikasi uji", "Halo {{.recipientName}},\n\nIni adalah notifikasi uji yang dikirim oleh {{.senderName}} pada {{.sentAt}}."},
	},
	"platform.venue_activated": {
		"en": {"Venue activated: {{.venueName}}", "Venue {{.venueName}} ({{.venueCode}}) is now Active."},
		"id": {"Venue aktif: {{.venueName}}", "Venue {{.venueName}} ({{.venueCode}}) sekarang Active."},
	},
}

// DefaultTemplates returns the seed templates for every channel.
func DefaultTemplates() []provision.Template {
	var out []provision.Template
	for event, locales := range defaults {
		for loc, t := range locales {
			for _, ch := range []string{"email", "in_app"} {
				out = append(out, provision.Template{Event: event, Channel: ch, Locale: loc, Subject: t.subject, Body: t.body})
			}
		}
	}
	return out
}
