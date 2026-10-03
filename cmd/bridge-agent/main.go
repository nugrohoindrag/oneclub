// Command bridge-agent is the OneClub local bridge agent skeleton
// (FR-INT-07, Technical Doc §8.2). It runs inside the club network next to
// the hardware (locker, turnstile, ball dispenser) and connects outbound to
// the OneClub API, so hardware is never exposed to the internet.
//
//	ONECLUB_API_URL=https://backoffice.club.example ONECLUB_AGENT_TOKEN=ocb_... bridge-agent
//
// P0 sends heartbeats with the hardware inventory; command execution is
// added with the first hardware vendor (P2).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const version = "0.1.0"

type heartbeat struct {
	AgentVersion string           `json:"agentVersion"`
	Hardware     []map[string]any `json:"hardware"`
}

type reply struct {
	AgentID         string `json:"agentId"`
	NextHeartbeatIn int    `json:"nextHeartbeatInSeconds"`
	Commands        []any  `json:"commands"`
}

func main() {
	api := strings.TrimRight(os.Getenv("ONECLUB_API_URL"), "/")
	token := os.Getenv("ONECLUB_AGENT_TOKEN")
	if api == "" || !strings.HasPrefix(token, "ocb_") {
		fmt.Fprintln(os.Stderr, "set ONECLUB_API_URL and ONECLUB_AGENT_TOKEN (ocb_…)")
		os.Exit(2)
	}
	var hw []map[string]any
	if v := os.Getenv("ONECLUB_HARDWARE"); v != "" {
		_ = json.Unmarshal([]byte(v), &hw)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := &http.Client{Timeout: 15 * time.Second}
	wait := 0 * time.Second
	for {
		select {
		case <-ctx.Done():
			log.Info("bridge agent stopped")
			return
		case <-time.After(wait):
		}
		body, _ := json.Marshal(heartbeat{AgentVersion: version, Hardware: hw})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, api+"/api/v1/bridge/heartbeat", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		wait = 30 * time.Second
		if err != nil {
			log.Warn("heartbeat failed", "err", err)
			wait = 10 * time.Second
			continue
		}
		var r reply
		_ = json.NewDecoder(resp.Body).Decode(&r)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Error("heartbeat rejected", "status", resp.Status)
			continue
		}
		if r.NextHeartbeatIn > 0 {
			wait = time.Duration(r.NextHeartbeatIn) * time.Second
		}
		log.Info("heartbeat ok", "agent", r.AgentID, "commands", len(r.Commands))
	}
}
