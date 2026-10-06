package integration

import (
	"context"
	"strconv"
	"testing"
)

// The sandbox adapters keep the last messages in memory only (bounded).
func TestSandboxMessages(t *testing.T) {
	ctx := context.Background()
	wa := &mockMessaging{env: Env{Code: "mock-whatsapp", Log: func(context.Context, Call) {}}}
	named := map[string]string{"otpCode": "482913", "number": "QUO-T-1"}
	if _, err := wa.SendMessage(ctx, OutboundMessage{To: "+62811", Template: "crm.quotation_otp", Named: named, Text: "482913 is your code"}); err != nil {
		t.Fatal(err)
	}
	named["otpCode"] = "changed"
	if _, err := wa.SendMessage(ctx, OutboundMessage{Template: "x", Named: map[string]string{"number": "QUO-T-1"}}); err == nil {
		t.Fatal("a failed send is an error")
	}
	got := SandboxMessages(func(m SandboxMessage) bool { return m.Named["number"] == "QUO-T-1" })
	if len(got) != 1 || got[0].Named["otpCode"] != "482913" || got[0].Channel != "whatsapp" || got[0].To != "+62811" {
		t.Fatalf("sandbox: %+v", got)
	}
	for i := range sandboxKeep + 10 {
		recordSandbox(SandboxMessage{Channel: "email", To: strconv.Itoa(i)})
	}
	all := SandboxMessages(nil)
	if len(all) != sandboxKeep || all[len(all)-1].To != strconv.Itoa(sandboxKeep+9) {
		t.Fatalf("bounded: %d", len(all))
	}
}
