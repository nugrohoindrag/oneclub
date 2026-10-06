package journey

// Journey templates (FR-JRN-02..04) with the priority journeys of PRD P5
// §16 #15: (1) membership renewal H-60 / H-30 / H-7 with escalation to
// sales, (2) birthday with an offer by tier, (3) welcome of new members,
// (4) win-back after 60 days without a visit (§9.4), (5) follow-up after a
// banquet event; plus abandoned booking (usulan). Messages are Bahasa
// Indonesia (PRD P5 §12) and editable before activation.

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"oneclub/internal/kernel/errs"
)

// TemplateInfo describes a template.
type TemplateInfo struct {
	Template    string `json:"template" enum:"renewal,birthday,welcome,win_back,post_event,abandoned_booking"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Priority    int    `json:"priority" doc:"PRD P5 §16 #15 priority (0 = additional template)"`
}

// TemplateInfos lists the templates.
var TemplateInfos = []TemplateInfo{
	{"renewal", "JRN-RENEWAL", "Membership Renewal", "H-60 offer, H-30 and H-7 reminders, escalation to sales when not renewed; stops when the membership is renewed", 1},
	{"birthday", "JRN-BIRTHDAY", "Birthday", "Birthday greeting with an offer by loyalty tier", 2},
	{"welcome", "JRN-WELCOME", "Welcome New Member", "Welcome on activation, Member App tips, a nudge when there is no visit after 7 days", 3},
	{"win_back", "JRN-WINBACK", "Win-back (60 days)", "60 days without a visit: WhatsApp offer → wait 7 days → booked: bonus points and a sales note; not booked: F&B voucher", 4},
	{"post_event", "JRN-POSTEVENT", "Post-event Follow-up", "Thank-you and feedback after a banquet event, next-event offer and a sales follow-up task", 5},
	{"abandoned_booking", "JRN-ABANDONED", "Cancelled Booking Follow-up", "A cancelled tee time is followed by an invitation to book again; stops on a new booking", 0},
}

func sp(s string) *string { return &s }
func ip(i int) *int       { return &i }
func pp(i int64) *int64   { return &i }

// TemplateInput returns the definition of a template (voucherType is the
// Commercial voucher type of the win-back F&B voucher).
func TemplateInput(template, voucherType string) (JourneyInput, error) {
	var info *TemplateInfo
	for i := range TemplateInfos {
		if TemplateInfos[i].Template == template {
			info = &TemplateInfos[i]
		}
	}
	if info == nil {
		return JourneyInput{}, errs.Validation("invalid_template", "unknown journey template", errs.Field("template", "invalid", "unknown template"))
	}
	in := JourneyInput{Code: info.Code, Name: info.Name, Description: info.Description, Category: "marketing", GoalDays: 7}
	switch template {
	case "renewal":
		in.TriggerType, in.TriggerDate, in.TriggerDays = "date", "membership_expiry", 60
		in.ExitEvents, in.GoalEvent, in.GoalDays = []string{"membership.renewed"}, "membership.renewed", 60
		in.Steps = []JourneyStep{
			{Key: "offer_h60", StepType: "message", Name: "H-60 renewal offer", Channel: sp("whatsapp"), OfferTitle: sp("Early renewal offer"),
				Subject: sp("Perpanjang keanggotaan Anda"), OfferValidDays: ip(30),
				Body: sp("Halo {{.name}}, keanggotaan {{.detail}} Anda berakhir pada {{.date}}. Perpanjang lebih awal dan nikmati penawaran khusus perpanjangan.")},
			{Key: "wait_h30", StepType: "wait", Name: "Until H-30", UntilDaysBefore: ip(30)},
			{Key: "check_h30", StepType: "condition", Name: "Renewed?", ConditionKind: sp("renewed"), OnTrue: sp(TargetExit)},
			{Key: "reminder_h30", StepType: "message", Name: "H-30 reminder", Channel: sp("whatsapp"), Subject: sp("Keanggotaan berakhir 30 hari lagi"),
				Body: sp("Halo {{.name}}, keanggotaan {{.detail}} Anda berakhir pada {{.date}}. Perpanjang sekarang agar tetap menikmati hak anggota.")},
			{Key: "wait_h7", StepType: "wait", Name: "Until H-7", UntilDaysBefore: ip(7)},
			{Key: "check_h7", StepType: "condition", Name: "Renewed?", ConditionKind: sp("renewed"), OnTrue: sp(TargetExit)},
			{Key: "reminder_h7", StepType: "message", Name: "H-7 reminder", Channel: sp("email"), Subject: sp("Keanggotaan Anda berakhir {{.date}}"),
				Body: sp("Halo {{.name}}, keanggotaan {{.detail}} Anda berakhir dalam 7 hari ({{.date}}). Hubungi kami atau perpanjang melalui Member App.")},
			{Key: "wait_h0", StepType: "wait", Name: "Until the end date", UntilDaysBefore: ip(0)},
			{Key: "check_h0", StepType: "condition", Name: "Renewed?", ConditionKind: sp("renewed"), OnTrue: sp(TargetExit)},
			{Key: "escalate", StepType: "sales_task", Name: "Escalate to sales", TaskSubject: sp("Membership not renewed: call {{.name}} (ended {{.date}})"),
				TaskDueDays: ip(1)},
		}
	case "birthday":
		in.TriggerType, in.TriggerDate, in.TriggerDays = "date", "birthday", 0
		in.GoalEvent, in.GoalDays, in.ReEntryDays = "billing.payment_settled", 30, 300
		in.Steps = []JourneyStep{
			{Key: "by_tier", StepType: "condition", Name: "Gold or Platinum?", ConditionKind: sp("tier"), ConditionValue: sp("GOLD,PLATINUM"),
				OnFalse: sp("greeting")},
			{Key: "greeting_vip", StepType: "message", Name: "Birthday offer (Gold / Platinum)", Channel: sp("whatsapp"), OfferTitle: sp("Birthday treat: 20% F&B"),
				PromoCode: sp("BDAYVIP"), OfferValidDays: ip(30), NextKey: sp(TargetEnd), Subject: sp("Selamat ulang tahun, {{.name}}!"),
				Body: sp("Selamat ulang tahun, {{.name}}! Sebagai member {{.offer}}, nikmati hadiah ulang tahun dengan kode {{.promoCode}} selama 30 hari.")},
			{Key: "greeting", StepType: "message", Name: "Birthday offer", Channel: sp("whatsapp"), OfferTitle: sp("Birthday treat: 10% F&B"),
				PromoCode: sp("BDAY"), OfferValidDays: ip(30), Subject: sp("Selamat ulang tahun, {{.name}}!"),
				Body: sp("Selamat ulang tahun, {{.name}}! Rayakan bersama kami dengan kode {{.promoCode}} selama 30 hari.")},
		}
	case "welcome":
		in.TriggerType, in.TriggerEvent = "event", "membership.activated"
		in.GoalEvent, in.GoalDays = "golf.booking_confirmed", 30
		in.Steps = []JourneyStep{
			{Key: "welcome", StepType: "message", Name: "Welcome", Channel: sp("email"), Subject: sp("Selamat datang, {{.name}}"),
				Body: sp("Selamat datang di Modern Golf & Country Club, {{.name}}! Keanggotaan Anda sudah aktif. Pesan tee time dan kelas melalui Member App.")},
			{Key: "wait_3d", StepType: "wait", Name: "3 days", WaitDays: ip(3)},
			{Key: "app_tips", StepType: "message", Name: "Member App tips", Channel: sp("in_app"), OfferTitle: sp("Your member benefits"),
				Body: sp("Halo {{.name}}, lihat benefit anggota, poin loyalty dan penawaran Anda di Member App.")},
			{Key: "wait_7d", StepType: "wait", Name: "7 days", WaitDays: ip(7)},
			{Key: "visited", StepType: "condition", Name: "Booked or visited?", ConditionKind: sp("booked"), OnTrue: sp(TargetEnd)},
			{Key: "nudge", StepType: "message", Name: "First visit nudge", Channel: sp("whatsapp"), Subject: sp("Tee time pertama Anda"),
				Body: sp("Halo {{.name}}, kami menunggu kunjungan pertama Anda. Pesan tee time pertama Anda melalui Member App.")},
		}
	case "win_back":
		in.TriggerType, in.TriggerDate, in.TriggerDays = "date", "last_visit", 60
		in.GoalEvent, in.GoalDays, in.ReEntryDays = "billing.payment_settled", 30, 90
		in.Steps = []JourneyStep{
			{Key: "offer", StepType: "message", Name: "Win-back offer", Channel: sp("whatsapp"), OfferTitle: sp("We miss you"),
				Subject: sp("Kami merindukan Anda"), OfferValidDays: ip(14),
				Body: sp("Halo {{.name}}, sudah lama kami tidak bertemu. Pesan tee time minggu ini dan nikmati penawaran khusus untuk Anda.")},
			{Key: "wait_7d", StepType: "wait", Name: "7 days", WaitDays: ip(7)},
			{Key: "booked", StepType: "condition", Name: "Booked?", ConditionKind: sp("booked"), OnFalse: sp("fnb_voucher")},
			{Key: "bonus", StepType: "points", Name: "Bonus points", Points: pp(200)},
			{Key: "note", StepType: "sales_task", Name: "Sales note", TaskSubject: sp("Win-back: {{.name}} is back — welcome call"), TaskDueDays: ip(2),
				NextKey: sp(TargetEnd)},
			{Key: "fnb_voucher", StepType: "voucher", Name: "F&B voucher", VoucherTypeRef: sp(voucherType)},
			{Key: "voucher_msg", StepType: "message", Name: "Voucher message", Channel: sp("whatsapp"), OfferTitle: sp("F&B voucher for you"),
				OfferValidDays: ip(30), Body: sp("Halo {{.name}}, kami kirimkan voucher F&B untuk kunjungan Anda berikutnya. Sampai jumpa di club!")},
		}
	case "post_event":
		in.TriggerType, in.TriggerEvent = "event", "banquet.event_completed"
		in.GoalEvent, in.GoalDays = "banquet.event_confirmed", 180
		in.Steps = []JourneyStep{
			{Key: "wait_1d", StepType: "wait", Name: "1 day", WaitDays: ip(1)},
			{Key: "thanks", StepType: "message", Name: "Thank you & feedback", Channel: sp("email"), Subject: sp("Terima kasih, {{.name}}"),
				Body: sp("Terima kasih telah merayakan acara Anda bersama kami, {{.name}}. Kami ingin mendengar pendapat Anda.")},
			{Key: "wait_14d", StepType: "wait", Name: "14 days", WaitDays: ip(14)},
			{Key: "next_event", StepType: "message", Name: "Next event offer", Channel: sp("email"), OfferTitle: sp("Your next event"),
				Body: sp("Halo {{.name}}, rencanakan acara berikutnya bersama kami dan dapatkan penawaran khusus untuk tamu kembali.")},
			{Key: "follow_up", StepType: "sales_task", Name: "Sales follow-up", TaskSubject: sp("Post-event follow-up: {{.name}}"), TaskDueDays: ip(3)},
		}
	case "abandoned_booking":
		in.TriggerType, in.TriggerEvent = "event", "golf.booking_cancelled"
		in.ExitEvents, in.GoalEvent, in.GoalDays = []string{"golf.booking_confirmed"}, "golf.booking_confirmed", 14
		in.Steps = []JourneyStep{
			{Key: "wait_2h", StepType: "wait", Name: "2 hours", WaitHours: ip(2)},
			{Key: "invite", StepType: "message", Name: "Book again", Channel: sp("in_app"), OfferTitle: sp("Still want to play?"),
				Body: sp("Halo {{.name}}, tee time Anda dibatalkan. Masih ingin bermain? Pilih tee time lain di Member App.")},
		}
	}
	return in, nil
}

// TemplateRequest creates a journey from a template.
type TemplateRequest struct {
	Template       string `json:"template" enum:"renewal,birthday,welcome,win_back,post_event,abandoned_booking"`
	Code           string `json:"code,omitempty" doc:"Default: the template code"`
	Name           string `json:"name,omitempty"`
	VoucherTypeRef string `json:"voucherTypeRef,omitempty" doc:"Win-back: Commercial voucher type of the F&B voucher"`
}

// FromTemplate creates a draft journey from a template.
func (s *Service) FromTemplate(ctx context.Context, tx pgx.Tx, property uuid.UUID, r TemplateRequest) (Journey, error) {
	vt := r.VoucherTypeRef
	if vt == "" {
		vt = DefaultVoucherType
	}
	in, err := TemplateInput(r.Template, vt)
	if err != nil {
		return Journey{}, err
	}
	if r.Code != "" {
		in.Code = r.Code
	}
	if r.Name != "" {
		in.Name = r.Name
	}
	return s.Create(ctx, tx, property, in, r.Template)
}

// DefaultVoucherType is the voucher type of the win-back template (seeded
// by the demo data).
const DefaultVoucherType = "JRN-FNB100"
