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
	// ── P1 (FR-INT-P1-03): golf, payment and membership ────────────────────
	"auth.login_code": {
		"en": {"Your login code: {{.code}}", "Hello {{.name}},\n\nYour one-time login code is {{.code}}. It expires in {{.expiresInMinutes}} minutes. Never share this code."},
		"id": {"Kode masuk Anda: {{.code}}", "Halo {{.name}},\n\nKode masuk sekali pakai Anda adalah {{.code}}. Berlaku {{.expiresInMinutes}} menit. Jangan bagikan kode ini kepada siapa pun."},
	},
	"auth.portal_activation": {
		"en": {"Activate your Member Portal account", "Hello {{.name}},\n\nYour Member Portal account is ready. Set your password with the link below (valid for {{.expiresInHours}} hours):\n\n{{.link}}"},
		"id": {"Aktivasi akun Member Portal", "Halo {{.name}},\n\nAkun Member Portal Anda sudah siap. Atur kata sandi melalui tautan berikut (berlaku {{.expiresInHours}} jam):\n\n{{.link}}"},
	},
	"golf.booking_confirmed": {
		"en": {"Booking confirmed {{.bookingCode}}", "Hello {{.name}},\n\nYour tee time is confirmed.\n\nBooking code: {{.bookingCode}}\nTee time: {{.teeTime}} ({{.course}})\nPlayers: {{.players}}\nTotal: {{.total}}\n\nShow the QR at check-in: {{.link}}"},
		"id": {"Booking terkonfirmasi {{.bookingCode}}", "Halo {{.name}},\n\nTee time Anda sudah terkonfirmasi.\n\nKode booking: {{.bookingCode}}\nTee time: {{.teeTime}} ({{.course}})\nPemain: {{.players}}\nTotal: {{.total}}\n\nTunjukkan QR saat check-in: {{.link}}"},
	},
	"golf.booking_payment_pending": {
		"en": {"Complete your payment — {{.bookingCode}}", "Hello {{.name}},\n\nYour tee time {{.teeTime}} is held until {{.dueAt}}. Pay {{.total}} to confirm it:\n\n{{.payLink}}"},
		"id": {"Selesaikan pembayaran — {{.bookingCode}}", "Halo {{.name}},\n\nTee time {{.teeTime}} Anda ditahan sampai {{.dueAt}}. Bayar {{.total}} untuk mengonfirmasi:\n\n{{.payLink}}"},
	},
	"golf.booking_cancelled": {
		"en": {"Booking cancelled {{.bookingCode}}", "Hello {{.name}},\n\nBooking {{.bookingCode}} for {{.teeTime}} has been cancelled.{{if .fee}}\nCancellation fee: {{.fee}}{{end}}{{if .refund}}\nRefund: {{.refund}}{{end}}{{if .reason}}\nReason: {{.reason}}{{end}}"},
		"id": {"Booking dibatalkan {{.bookingCode}}", "Halo {{.name}},\n\nBooking {{.bookingCode}} untuk {{.teeTime}} telah dibatalkan.{{if .fee}}\nBiaya pembatalan: {{.fee}}{{end}}{{if .refund}}\nRefund: {{.refund}}{{end}}{{if .reason}}\nAlasan: {{.reason}}{{end}}"},
	},
	"golf.booking_rescheduled": {
		"en": {"Booking rescheduled {{.bookingCode}}", "Hello {{.name}},\n\nBooking {{.bookingCode}} moved from {{.oldTeeTime}} to {{.teeTime}} ({{.course}}).\nTotal: {{.total}}\n\n{{.link}}"},
		"id": {"Jadwal booking diubah {{.bookingCode}}", "Halo {{.name}},\n\nBooking {{.bookingCode}} dipindahkan dari {{.oldTeeTime}} ke {{.teeTime}} ({{.course}}).\nTotal: {{.total}}\n\n{{.link}}"},
	},
	"golf.booking_reminder": {
		"en": {"Reminder: tee time tomorrow {{.teeTime}}", "Hello {{.name}},\n\nSee you tomorrow at {{.teeTime}} ({{.course}}), booking {{.bookingCode}}. Please arrive 30 minutes early for check-in and bag drop.\n\n{{.link}}"},
		"id": {"Pengingat: tee time besok {{.teeTime}}", "Halo {{.name}},\n\nSampai jumpa besok pukul {{.teeTime}} ({{.course}}), booking {{.bookingCode}}. Mohon datang 30 menit lebih awal untuk check-in dan bag drop.\n\n{{.link}}"},
	},
	"golf.booking_no_show": {
		"en": {"Missed tee time {{.bookingCode}}", "Hello {{.name}},\n\nWe did not see you at {{.teeTime}} (booking {{.bookingCode}}). It has been recorded as No-show.{{if .fee}}\nNo-show fee: {{.fee}}{{end}}"},
		"id": {"Tee time terlewat {{.bookingCode}}", "Halo {{.name}},\n\nKami tidak melihat Anda pada {{.teeTime}} (booking {{.bookingCode}}). Booking dicatat sebagai No-show.{{if .fee}}\nBiaya no-show: {{.fee}}{{end}}"},
	},
	"golf.rain_check_issued": {
		"en": {"Rain Check {{.number}}", "Hello {{.name}},\n\nPlay was stopped by the weather. Rain Check {{.number}} worth {{.amount}} has been issued and is valid until {{.expiresOn}}. Use it on your next booking."},
		"id": {"Rain Check {{.number}}", "Halo {{.name}},\n\nPermainan dihentikan karena cuaca. Rain Check {{.number}} senilai {{.amount}} telah diterbitkan dan berlaku sampai {{.expiresOn}}. Gunakan pada booking berikutnya."},
	},
	"golf.course_status_changed": {
		"en": {"Course status: {{.course}} — {{.summary}}", "{{.course}} is now {{.courseState}}. Weather: {{.weather}}. Lights: {{.lighting}}.{{if .closedHoles}}\nClosed holes: {{.closedHoles}}{{end}}{{if .notes}}\n{{.notes}}{{end}}"},
		"id": {"Status lapangan: {{.course}} — {{.summary}}", "{{.course}} sekarang {{.courseState}}. Cuaca: {{.weather}}. Lampu: {{.lighting}}.{{if .closedHoles}}\nHole ditutup: {{.closedHoles}}{{end}}{{if .notes}}\n{{.notes}}{{end}}"},
	},
	"golf.bookings_affected": {
		"en": {"{{.count}} bookings affected by {{.reason}}", "A {{.reason}} block on {{.course}} ({{.period}}) affects {{.count}} bookings. Follow them up in Golf → Bookings: {{.link}}"},
		"id": {"{{.count}} booking terdampak {{.reason}}", "Blok {{.reason}} pada {{.course}} ({{.period}}) berdampak pada {{.count}} booking. Tindak lanjuti di Golf → Bookings: {{.link}}"},
	},
	"billing.receipt": {
		"en": {"Receipt {{.number}}", "Hello {{.name}},\n\nThank you. We received {{.amount}} by {{.method}} on {{.paidAt}}.\nReceipt: {{.number}}{{if .reference}} ({{.reference}}){{end}}\n\n{{.link}}"},
		"id": {"Bukti bayar {{.number}}", "Halo {{.name}},\n\nTerima kasih. Kami telah menerima {{.amount}} melalui {{.method}} pada {{.paidAt}}.\nBukti bayar: {{.number}}{{if .reference}} ({{.reference}}){{end}}\n\n{{.link}}"},
	},
	"billing.refund_processed": {
		"en": {"Refund {{.number}} processed", "Hello {{.name}},\n\nA refund of {{.amount}} to {{.destination}} has been processed.{{if .reason}}\nReason: {{.reason}}{{end}}"},
		"id": {"Refund {{.number}} diproses", "Halo {{.name}},\n\nRefund sebesar {{.amount}} ke {{.destination}} telah diproses.{{if .reason}}\nAlasan: {{.reason}}{{end}}"},
	},
	"membership.application_submitted": {
		"en": {"Membership application {{.number}} received", "Hello {{.name}},\n\nWe received your {{.type}} membership application ({{.number}}). We will inform you once it has been reviewed."},
		"id": {"Aplikasi membership {{.number}} diterima", "Halo {{.name}},\n\nAplikasi membership {{.type}} Anda ({{.number}}) sudah kami terima. Kami akan menginformasikan hasil peninjauannya."},
	},
	"membership.application_approved": {
		"en": {"Membership application approved", "Hello {{.name}},\n\nYour {{.type}} membership application ({{.number}}) has been approved. Membership fee: {{.fee}}. Your membership is activated once the fee is paid."},
		"id": {"Aplikasi membership disetujui", "Halo {{.name}},\n\nAplikasi membership {{.type}} Anda ({{.number}}) telah disetujui. Membership fee: {{.fee}}. Membership aktif setelah fee dibayar."},
	},
	"membership.application_rejected": {
		"en": {"Membership application {{.number}}", "Hello {{.name}},\n\nWe are sorry, your membership application ({{.number}}) was not approved.{{if .reason}}\nReason: {{.reason}}{{end}}"},
		"id": {"Aplikasi membership {{.number}}", "Halo {{.name}},\n\nMohon maaf, aplikasi membership Anda ({{.number}}) belum dapat disetujui.{{if .reason}}\nAlasan: {{.reason}}{{end}}"},
	},
	"membership.activated": {
		"en": {"Welcome, member {{.memberNo}}", "Hello {{.name}},\n\nYour {{.type}} membership is Active until {{.validUntil}}. Member number: {{.memberNo}}. Your Digital Member Card is in the Member Portal: {{.link}}"},
		"id": {"Selamat datang, member {{.memberNo}}", "Halo {{.name}},\n\nMembership {{.type}} Anda Active sampai {{.validUntil}}. Nomor member: {{.memberNo}}. Digital Member Card tersedia di Member Portal: {{.link}}"},
	},
	"membership.renewal_reminder": {
		"en": {"Your membership expires in {{.daysLeft}} days", "Hello {{.name}},\n\nYour {{.type}} membership ({{.memberNo}}) expires on {{.endsOn}}. Renew to keep your Member Rate and privileges."},
		"id": {"Membership Anda berakhir dalam {{.daysLeft}} hari", "Halo {{.name}},\n\nMembership {{.type}} Anda ({{.memberNo}}) berakhir pada {{.endsOn}}. Perpanjang agar tetap mendapatkan Member Rate dan hak member."},
	},
	"membership.renewed": {
		"en": {"Membership renewed until {{.validUntil}}", "Hello {{.name}},\n\nThank you for renewing. Your membership ({{.memberNo}}) is valid until {{.validUntil}}."},
		"id": {"Membership diperpanjang sampai {{.validUntil}}", "Halo {{.name}},\n\nTerima kasih telah memperpanjang. Membership Anda ({{.memberNo}}) berlaku sampai {{.validUntil}}."},
	},
	"membership.statement_ready": {
		"en": {"Member Statement {{.period}}", "Hello {{.name}},\n\nYour Member Statement for {{.period}} is ready. Closing balance: {{.closingBalance}}.\n\n{{.link}}"},
		"id": {"Member Statement {{.period}}", "Halo {{.name}},\n\nMember Statement periode {{.period}} sudah tersedia. Saldo akhir: {{.closingBalance}}.\n\n{{.link}}"},
	},
}

// whatsAppEvents are customer-facing P1 events also sent by WhatsApp
// (FR-INT-P1-02); WhatsApp template texts are kept short in the BSP and
// mapped per event in the integration settings.
var whatsAppEvents = map[string]bool{"auth.login_code": true, "auth.portal_activation": true, "golf.booking_confirmed": true,
	"golf.booking_payment_pending": true, "golf.booking_cancelled": true, "golf.booking_rescheduled": true, "golf.booking_reminder": true,
	"golf.booking_no_show": true, "golf.rain_check_issued": true, "billing.receipt": true, "billing.refund_processed": true,
	"membership.application_submitted": true, "membership.application_approved": true, "membership.application_rejected": true,
	"membership.activated": true, "membership.renewal_reminder": true, "membership.renewed": true}

// DefaultTemplates returns the seed templates for every channel.
func DefaultTemplates() []provision.Template {
	var out []provision.Template
	for event, locales := range defaults {
		for loc, t := range locales {
			chs := []string{"email", "in_app"}
			if whatsAppEvents[event] {
				chs = append(chs, "whatsapp")
			}
			for _, ch := range chs {
				out = append(out, provision.Template{Event: event, Channel: ch, Locale: loc, Subject: t.subject, Body: t.body})
			}
		}
	}
	return out
}
