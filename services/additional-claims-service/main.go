package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

const (
	defaultPort       = "4012"
	defaultConfigPath = "/config/claims.json"
	maxRequestBytes   = 1 << 20
)

type claimRule struct {
	OutwayPeerID  string         `json:"outway_peer_id"`
	ServicePeerID string         `json:"service_peer_id"`
	ServiceName   string         `json:"service_name"`
	Add           map[string]any `json:"add"`
}

type claimsConfig struct {
	Rules []claimRule `json:"rules"`
}

type tokenRequest struct {
	Grant *struct {
		Data grantData `json:"data"`
	} `json:"grant"`
}

type grantData struct {
	Type   string `json:"type"`
	Outway struct {
		PeerID string `json:"peer_id"`
	} `json:"outway"`
	Service struct {
		PeerID string `json:"peer_id"`
		Name   string `json:"name"`
	} `json:"service"`
}

type tokenResponse struct {
	Add map[string]any `json:"add"`
}

type claimsService struct {
	rules []claimRule
}

func loadClaimsConfig(path string) (claimsConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return claimsConfig{}, fmt.Errorf("read claims config: %w", err)
	}

	var cfg claimsConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return claimsConfig{}, fmt.Errorf("decode claims config: %w", err)
	}

	seen := make(map[string]struct{}, len(cfg.Rules))
	for i, rule := range cfg.Rules {
		if rule.OutwayPeerID == "" || rule.ServicePeerID == "" || rule.ServiceName == "" {
			return claimsConfig{}, fmt.Errorf("rule %d must define outway_peer_id, service_peer_id and service_name", i)
		}
		if len(rule.Add) == 0 {
			return claimsConfig{}, fmt.Errorf("rule %d must define at least one additional claim", i)
		}

		key := rule.OutwayPeerID + "\x00" + rule.ServicePeerID + "\x00" + rule.ServiceName
		if _, ok := seen[key]; ok {
			return claimsConfig{}, fmt.Errorf("rule %d duplicates an earlier grant mapping", i)
		}
		seen[key] = struct{}{}
	}

	return cfg, nil
}

func (s claimsService) claimsForGrant(grant grantData) (map[string]any, bool) {
	for _, rule := range s.rules {
		if rule.OutwayPeerID == grant.Outway.PeerID &&
			rule.ServicePeerID == grant.Service.PeerID &&
			rule.ServiceName == grant.Service.Name {
			return rule.Add, true
		}
	}

	return nil, false
}

func (s claimsService) handleAdditionalClaims(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxRequestBytes))

	var req tokenRequest
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "request body must contain one JSON document")
		return
	}

	if req.Grant == nil {
		writeError(w, http.StatusBadRequest, "grant is required")
		return
	}

	switch req.Grant.Data.Type {
	case "GRANT_TYPE_SERVICE_CONNECTION", "GRANT_TYPE_DELEGATED_SERVICE_CONNECTION":
	default:
		writeError(w, http.StatusBadRequest, "unsupported grant type")
		return
	}

	claims, ok := s.claimsForGrant(req.Grant.Data)
	if !ok {
		slog.Warn("no additional-claims mapping for grant",
			"outway_peer_id", req.Grant.Data.Outway.PeerID,
			"service_peer_id", req.Grant.Data.Service.PeerID,
			"service_name", req.Grant.Data.Service.Name,
		)
		writeError(w, http.StatusUnprocessableEntity, "grant is not configured")
		return
	}

	slog.Info("additional claims resolved",
		"outway_peer_id", req.Grant.Data.Outway.PeerID,
		"service_peer_id", req.Grant.Data.Service.PeerID,
		"service_name", req.Grant.Data.Service.Name,
	)
	writeJSON(w, http.StatusOK, tokenResponse{Add: claims})
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Error("write JSON response", "err", err)
	}
}

func newMux(service claimsService) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/additional-claims", service.handleAdditionalClaims)

	return mux
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return fallback
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "additional-claims-service"))

	configPath := getEnv("CLAIMS_CONFIG_PATH", defaultConfigPath)
	cfg, err := loadClaimsConfig(configPath)
	if err != nil {
		slog.Error("load configuration", "path", configPath, "err", err)
		os.Exit(1)
	}

	port := getEnv("PORT", defaultPort)
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           newMux(claimsService{rules: cfg.Rules}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	slog.Info("additional claims service starting", "port", port, "rules", len(cfg.Rules))
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
