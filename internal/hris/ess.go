package hris

// Employee Self Service section registry (PRD P5 EP-16, §7.2): ESS is the
// personal-login area of the ops shell (/ops/ess). Core HR registers
// Profile, My Documents, My Training and the manager's Team; the time and
// payroll areas register My Schedule, Clock In / Out, Attendance History,
// Leave & Permission, Overtime, Payslip and the manager's Approvals and Team
// Attendance from their own package init (or internal/app). GET
// /api/v1/ess/me returns the sections the employee may open; the Staff App
// renders a registered section by its Key (web/apps/staff/src/p5/hr.tsx
// registerEssSection).

import (
	"slices"
	"sort"
	"sync"
)

// ESSSection is one section of Employee Self Service.
type ESSSection struct {
	Key        string `json:"key" doc:"Stable key; the Staff App maps it to its screen"`
	Label      string `json:"label" doc:"English label (Naming Convention)"`
	LabelID    string `json:"labelId" doc:"Bahasa Indonesia label"`
	Icon       string `json:"icon" doc:"Material Symbols Rounded name"`
	Path       string `json:"path" doc:"Staff App path, e.g. /ops/ess/schedule"`
	Permission string `json:"-"`
	// Module gates the section on an enabled module ("" = hris).
	Module string `json:"-"`
	// Manager sections are shown to employees who lead a team only.
	Manager bool `json:"manager"`
	Order   int  `json:"order"`
	// Offline: the section may be cached for offline use; payroll data
	// never is (FR-OPS-P5-05).
	Offline bool `json:"offline"`
}

var (
	essMu       sync.RWMutex
	essSections []ESSSection
)

// PermissionESS is the permission of the personal ESS area; PermissionTeam
// opens the manager view and PermissionTeamApprove lets a department head
// approve the requests of their team (leave, overtime, shift swaps,
// attendance corrections).
const (
	PermissionESS         = "hris.ess.use"
	PermissionTeam        = "hris.team.view"
	PermissionTeamApprove = "hris.team.approve"
)

// RegisterESSSection adds (or replaces, by Key) an ESS section.
func RegisterESSSection(s ESSSection) {
	essMu.Lock()
	defer essMu.Unlock()
	if s.Permission == "" {
		s.Permission = PermissionESS
	}
	if i := slices.IndexFunc(essSections, func(x ESSSection) bool { return x.Key == s.Key }); i >= 0 {
		essSections[i] = s
		return
	}
	essSections = append(essSections, s)
}

// ESSSections returns the registered sections in display order.
func ESSSections() []ESSSection {
	essMu.RLock()
	defer essMu.RUnlock()
	out := slices.Clone(essSections)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func init() {
	for _, s := range []ESSSection{
		{Key: "profile", Label: "Profile", LabelID: "Profil", Icon: "badge", Path: "/ops/ess/profile", Order: 10, Offline: true},
		{Key: "documents", Label: "My Documents", LabelID: "Dokumen Saya", Icon: "folder_shared", Path: "/ops/ess/documents", Order: 70},
		{Key: "training", Label: "My Training", LabelID: "Pelatihan Saya", Icon: "school", Path: "/ops/ess/training", Order: 80, Offline: true},
		{Key: "team", Label: "My Team", LabelID: "Tim Saya", Icon: "groups", Path: "/ops/ess/team", Permission: PermissionTeam, Manager: true,
			Order: 100},
	} {
		RegisterESSSection(s)
	}
}
