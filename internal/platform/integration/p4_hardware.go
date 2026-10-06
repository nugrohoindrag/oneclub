package integration

// Hardware deferred from P2 (PRD P4 FR-INT-P4-04, Should; Technical Doc
// decision #7; open question #14 — vendors not selected yet). Hardware is
// never exposed to the internet: the local bridge agent (Go) at the club
// connects outbound to OneClub, reports its devices in the heartbeat and
// receives queued commands (bridge_commands.go, P2). P4 fixes the device
// profiles every vendor driver of the agent implements, so a vendor is a
// driver on the agent side without changes in OneClub:
//
//   - locker (electronic RFID locker banks, e.g. sport club & golf locker
//     rooms): assign a locker to a card, release it, open it remotely;
//   - turnstile and access devices (one vendor with the attendance
//     terminals, e.g. ZKTeco): open once, grant / revoke a card, push the
//     whole access list after an outage;
//   - ball dispenser (relay or vendor API): dispense a bucket;
//   - golf cart GPS: positions arrive through the golf API (P2); remote
//     commands are deferred with the GPS vendor (Should).
//
// Modules validate a command against the profile (ValidateHardwareCommand)
// before bridge Enqueue; the agent answers with the result fields listed per
// command. Results and payloads are logged like every integration call.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// HardwareParam is one payload field of a command.
type HardwareParam struct {
	Key      string   `json:"key"`
	Type     string   `json:"type" enum:"string,int,bool,list"`
	Required bool     `json:"required"`
	Enum     []string `json:"enum,omitempty"`
	Max      int      `json:"max,omitempty" doc:"Maximum value (int) or length (string, list)"`
}

// HardwareCommandSpec is a command of a device profile.
type HardwareCommandSpec struct {
	Command     string          `json:"command"`
	Description string          `json:"description"`
	Params      []HardwareParam `json:"params"`
	Result      []string        `json:"result" doc:"Fields the agent reports back"`
	TTLSeconds  int             `json:"ttlSeconds" doc:"Command expires when the agent has not picked it up"`
}

// HardwareProfile is the contract between OneClub and the agent drivers of
// one device kind.
type HardwareProfile struct {
	Kind        string                `json:"kind"`
	Name        string                `json:"name"`
	Status      string                `json:"status" enum:"available,deferred"`
	Vendors     string                `json:"vendors" doc:"Vendor examples (open question #14)"`
	Description string                `json:"description"`
	Commands    []HardwareCommandSpec `json:"commands"`
}

// HardwareProfiles are the device profiles of the bridge agent.
var HardwareProfiles = []HardwareProfile{
	{Kind: "locker", Name: "Electronic locker (RFID)", Status: "available", Vendors: "RFID locker controllers (RS-485 / TCP)",
		Description: "Locker banks of the sport club and golf locker rooms; a member card opens the assigned locker.",
		Commands: []HardwareCommandSpec{
			{Command: "assign", Description: "Assign a locker to a card until a time", TTLSeconds: 60,
				Params: []HardwareParam{{Key: "locker", Type: "string", Required: true, Max: 20}, {Key: "cardUid", Type: "string", Required: true, Max: 32},
					{Key: "until", Type: "string", Max: 40}}, Result: []string{"locker", "assigned"}},
			{Command: "release", Description: "Release a locker (end of visit)", TTLSeconds: 60,
				Params: []HardwareParam{{Key: "locker", Type: "string", Required: true, Max: 20}}, Result: []string{"locker", "released"}},
			{Command: "open", Description: "Open a locker once (staff override)", TTLSeconds: 30,
				Params: []HardwareParam{{Key: "locker", Type: "string", Required: true, Max: 20}, {Key: "reason", Type: "string", Required: true, Max: 200}},
				Result: []string{"locker", "opened"}},
		}},
	{Kind: "turnstile", Name: "Turnstile & access terminal", Status: "available", Vendors: "ZKTeco (same vendor as the attendance terminals, P5)",
		Description: "Entrance of the sport club / pool; members and valid visit passes enter with card or QR.",
		Commands: []HardwareCommandSpec{
			{Command: "open", Description: "Let one person through", TTLSeconds: 15,
				Params: []HardwareParam{{Key: "direction", Type: "string", Enum: []string{"in", "out"}}, {Key: "reason", Type: "string", Max: 200}},
				Result: []string{"opened"}},
			{Command: "grant", Description: "Allow a card / QR until a time", TTLSeconds: 120,
				Params: []HardwareParam{{Key: "credential", Type: "string", Required: true, Max: 64}, {Key: "until", Type: "string", Max: 40},
					{Key: "zones", Type: "list", Max: 20}}, Result: []string{"credential", "granted"}},
			{Command: "revoke", Description: "Remove a card / QR", TTLSeconds: 120,
				Params: []HardwareParam{{Key: "credential", Type: "string", Required: true, Max: 64}}, Result: []string{"credential", "revoked"}},
			{Command: "sync_access_list", Description: "Replace the device access list after an outage", TTLSeconds: 600,
				Params: []HardwareParam{{Key: "credentials", Type: "list", Required: true, Max: 20000}}, Result: []string{"count"}},
		}},
	{Kind: "ball_dispenser", Name: "Driving range ball dispenser", Status: "available", Vendors: "Relay board or vendor API",
		Description: "Dispenses a bucket after a range sale or a range card tap.",
		Commands: []HardwareCommandSpec{
			{Command: "dispense", Description: "Dispense a number of balls", TTLSeconds: 60,
				Params: []HardwareParam{{Key: "balls", Type: "int", Required: true, Max: 500}, {Key: "bay", Type: "string", Max: 20}},
				Result: []string{"dispensed"}},
		}},
	{Kind: "golf_cart_gps", Name: "Golf cart GPS", Status: "deferred", Vendors: "Not selected (Should)",
		Description: "Positions are posted to /api/v1/golf/golf-cart-positions (P2); remote commands (geofence alerts, speed limits) wait for the GPS vendor.",
		Commands:    []HardwareCommandSpec{}},
}

// HardwareProfileOf returns the profile of a device kind.
func HardwareProfileOf(kind string) (HardwareProfile, bool) {
	for _, p := range HardwareProfiles {
		if p.Kind == kind {
			return p, true
		}
	}
	return HardwareProfile{}, false
}

// ValidateHardwareCommand checks a command and its payload against the
// profile of the device kind (deferred kinds accept no commands).
func ValidateHardwareCommand(kind, command string, payload map[string]any) error {
	p, ok := HardwareProfileOf(kind)
	if !ok {
		return fmt.Errorf("unknown device kind %q", kind)
	}
	var spec *HardwareCommandSpec
	for i := range p.Commands {
		if p.Commands[i].Command == command {
			spec = &p.Commands[i]
		}
	}
	if spec == nil {
		var names []string
		for _, c := range p.Commands {
			names = append(names, c.Command)
		}
		if len(names) == 0 {
			return fmt.Errorf("%s accepts no commands yet (%s)", p.Name, p.Status)
		}
		return fmt.Errorf("%s accepts: %s", p.Name, strings.Join(names, ", "))
	}
	for k := range payload {
		if !slices.ContainsFunc(spec.Params, func(hp HardwareParam) bool { return hp.Key == k }) {
			return fmt.Errorf("%s %s: unknown field %s", kind, command, k)
		}
	}
	for _, hp := range spec.Params {
		v, present := payload[hp.Key]
		if !present || v == nil || v == "" {
			if hp.Required {
				return fmt.Errorf("%s %s: %s is required", kind, command, hp.Key)
			}
			continue
		}
		switch hp.Type {
		case "int":
			n, err := strconv.Atoi(strings.TrimSuffix(fmt.Sprint(v), ".0"))
			if err != nil || n < 1 || (hp.Max > 0 && n > hp.Max) {
				return fmt.Errorf("%s %s: %s must be a whole number from 1 to %d", kind, command, hp.Key, hp.Max)
			}
		case "bool":
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("%s %s: %s must be true or false", kind, command, hp.Key)
			}
		case "list":
			l, ok := v.([]any)
			if !ok {
				if s, sok := v.([]string); sok {
					l = make([]any, len(s))
				} else {
					return fmt.Errorf("%s %s: %s must be a list", kind, command, hp.Key)
				}
			}
			if hp.Max > 0 && len(l) > hp.Max {
				return fmt.Errorf("%s %s: %s has at most %d entries", kind, command, hp.Key, hp.Max)
			}
		default:
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("%s %s: %s must be text", kind, command, hp.Key)
			}
			if len(hp.Enum) > 0 && !slices.Contains(hp.Enum, s) {
				return fmt.Errorf("%s %s: %s must be one of %s", kind, command, hp.Key, strings.Join(hp.Enum, ", "))
			}
			if hp.Max > 0 && len([]rune(s)) > hp.Max {
				return fmt.Errorf("%s %s: %s has at most %d characters", kind, command, hp.Key, hp.Max)
			}
		}
	}
	return nil
}
