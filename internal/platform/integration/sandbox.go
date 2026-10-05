package integration

import (
	"maps"
	"sync"
	"time"
)

// SandboxMessage is a message "sent" by a sandbox adapter (mock WhatsApp,
// mock e-mail) or the development log mailer. Such messages never leave the
// process, so the sandbox keeps the last ones in memory only — like the
// inbox of a sandbox provider — for developers and the end-to-end tests,
// e.g. to read a one-time code that is erased from the delivery once sent
// (platform.notification_deliveries). Nothing here is persisted, logged or
// served by any route; production providers do not record anything.
type SandboxMessage struct {
	Integration string            // integration code (mock-whatsapp, mock-email, log-mailer)
	Channel     string            // whatsapp | email
	To          string            // phone or e-mail address
	Template    string            // event code (WhatsApp)
	Subject     string            // e-mail subject
	Text        string            // rendered text
	Named       map[string]string // template variables (WhatsApp)
	SentAt      time.Time
}

const sandboxKeep = 500

var (
	sandboxMu  sync.Mutex
	sandboxBox []SandboxMessage
)

func recordSandbox(m SandboxMessage) {
	m.Named = maps.Clone(m.Named)
	m.SentAt = time.Now()
	sandboxMu.Lock()
	defer sandboxMu.Unlock()
	sandboxBox = append(sandboxBox, m)
	if n := len(sandboxBox) - sandboxKeep; n > 0 {
		sandboxBox = append([]SandboxMessage(nil), sandboxBox[n:]...)
	}
}

// SandboxMessages returns the messages sent through sandbox adapters by
// this process (oldest first, at most the last 500) for which keep is true.
func SandboxMessages(keep func(SandboxMessage) bool) []SandboxMessage {
	sandboxMu.Lock()
	defer sandboxMu.Unlock()
	var out []SandboxMessage
	for _, m := range sandboxBox {
		if keep == nil || keep(m) {
			m.Named = maps.Clone(m.Named)
			out = append(out, m)
		}
	}
	return out
}
