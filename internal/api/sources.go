package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/slager2/qwenchik/internal/providers/research"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
	"github.com/slager2/qwenchik/internal/tools"
	"github.com/slager2/qwenchik/internal/urlcanon"
)

func sourceFingerprint(raw string) string {
	canonical, e := urlcanon.Canonical(raw)
	if e == nil {
		raw = canonical
	}
	hash := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(hash[:])
}

func (s *Server) sourceProjection(ctx context.Context, id string) ([]map[string]any, error) {
	evidence, err := s.service.Evidence(ctx, id)
	if err != nil {
		return nil, err
	}
	sources := []map[string]any{}
	byFingerprint := map[string]int{}
	byURL := map[string]int{}
	pendingFallback := map[string]int{}
	add := func(hit research.SearchHitObservation, status, evidenceID string, existingOnly bool) {
		errorCode := sourceErrorCode(hit.ErrorCode)
		if hit.Fingerprint == "" && hit.URL != "" {
			hit.Fingerprint = sourceFingerprint(hit.URL)
		}
		hit = tools.SafeSearchHitObservation(hit)
		index, found := byFingerprint[hit.Fingerprint]
		urlKey := ""
		if hit.URL != "" {
			urlKey = sourceFingerprint(hit.URL)
			if other, ok := byURL[urlKey]; ok {
				index, found = other, true
			}
		}
		if !found && existingOnly {
			return
		}
		if !found {
			index = len(sources)
			sources = append(sources, map[string]any{"id": hit.Fingerprint, "fingerprint": hit.Fingerprint})
		}
		item := sources[index]
		byFingerprint[hit.Fingerprint] = index
		if urlKey != "" {
			byURL[urlKey] = index
		}
		if status == "discovered" && item["status"] != nil && item["status"] != "discovered" {
			return
		}
		if status == "validated" && item["status"] != nil && item["status"] != "discovered" && item["status"] != "validated" {
			return
		}
		item["status"] = status
		item["error_code"] = errorCode
		item["retryable"] = hit.Retryable
		if hit.URL != "" {
			item["url"] = hit.URL
			if hit.Title != "" {
				item["title"] = hit.Title
			}
			if hit.Snippet != "" {
				item["snippet"] = hit.Snippet
			}
		}
		if hit.Origin != "" {
			item["origin"] = hit.Origin
		}
		if item["provider"] == nil || hit.Provider != "search" {
			item["provider"] = hit.Provider
		}
		if hit.QueryFingerprint != "" {
			item["query_fingerprint"] = hit.QueryFingerprint
		}
		if !hit.Time.IsZero() {
			item["time"] = hit.Time
		}
		if evidenceID != "" {
			item["evidence_id"] = evidenceID
		}
	}
	var latestVerification json.RawMessage
	after := int64(0)
	for {
		events, e := s.service.Events(ctx, id, after, 500)
		if e != nil {
			return nil, e
		}
		for _, event := range events {
			after = event.Seq
			if event.Type == "verification.updated" && event.Visibility == "user" {
				latestVerification = event.Payload
				continue
			}
			if event.Type == "search.hit" && event.Visibility == "user" {
				var p struct {
					Hit      *research.SearchHitObservation `json:"hit"`
					URL      string                         `json:"url"`
					Title    string                         `json:"title"`
					Provider string                         `json:"provider"`
				}
				if json.Unmarshal(event.Payload, &p) != nil {
					continue
				}
				if p.Hit == nil {
					if p.URL == "" {
						continue
					}
					card := tools.SafeSearchHitObservation(research.SearchHitObservation{URL: p.URL, Title: p.Title, Provider: p.Provider, Status: "discovered"})
					if card.URL == "" {
						continue
					}
					p.Hit = &research.SearchHitObservation{URL: p.URL, Title: p.Title, Provider: p.Provider, Status: "discovered"}
				}
				if p.Hit.URL == "" && !validFingerprint(p.Hit.Fingerprint) {
					continue
				}
				hit := tools.SafeSearchHitObservation(*p.Hit)
				if p.Hit.Fingerprint == "" {
					hit.Fingerprint = sourceFingerprint(p.Hit.URL)
				}
				add(hit, hit.Status, "", false)
				continue
			}
			p, ok := sourceLifecycleEvent(event)
			if !ok || (event.Visibility != "user" && event.Visibility != "audit") {
				continue
			}
			fingerprint, _ := p["source_fingerprint"].(string)
			rawURL, _ := p["target_url"].(string)
			status, _ := p["status"].(string)
			if fingerprint == "" || status == "" {
				continue
			}
			index, found := byFingerprint[fingerprint]
			if other, exists := byURL[sourceFingerprint(rawURL)]; rawURL != "" && exists {
				index, found = other, true
			}
			if found && status == "fetching" && sources[index]["attempt_id"] == p["attempt_id"] && p["attempt_id"] != nil && (sources[index]["status"] == "failed" || sources[index]["status"] == "rejected" || sources[index]["status"] == "extracted" || sources[index]["status"] == "cited") {
				continue
			}
			scope := fmt.Sprintf("%v:%v", p["plan_attempt"], p["step_id"])
			if previousIndex, exists := pendingFallback[scope]; exists && status == "fetching" && p["attempt_id"] != nil && p["plan_attempt"] != nil && p["step_id"] != nil {
				previous := sources[previousIndex]
				if previous["attempt_id"] != nil && previous["attempt_id"] != p["attempt_id"] && (!found || index != previousIndex) {
					previous["fallback_attempted"] = true
				}
			}
			code, _ := p["error_code"].(string)
			retryable, _ := p["retryable"].(bool)
			add(research.SearchHitObservation{URL: rawURL, Fingerprint: fingerprint, Status: "validated", ErrorCode: code, Retryable: retryable}, status, "", false)
			index = byFingerprint[fingerprint]
			item := sources[index]
			confirmedFallback := item["fallback_attempted"] == true && item["attempt_id"] == p["attempt_id"]
			if status == "fetching" {
				for _, key := range []string{"error_code", "declared_bytes", "effective_limit_bytes", "fetched_bytes", "mime_type", "try_next_source", "remaining_candidates", "fallback_attempted", "user_retry_task"} {
					delete(item, key)
				}
			}
			for key, value := range p {
				item[key] = value
			}
			if confirmedFallback {
				item["fallback_attempted"] = true
			}
			item["event_seq"] = event.Seq
			if (status == "failed" || status == "rejected") && item["try_next_source"] == true {
				pendingFallback[scope] = index
			}
		}
		if len(events) < 500 {
			break
		}
	}
	cited := map[string]bool{}
	verification := sqlite.SafeVerificationMetadata(latestVerification, evidence)
	if refs, ok := verification["citations"].([]map[string]string); ok {
		for _, ref := range refs {
			cited[ref["evidence_id"]] = true
		}
	}
	for _, e := range evidence {
		card := tools.SafeSearchHitObservation(research.SearchHitObservation{URL: e.CanonicalURL, Title: e.Title, Provider: e.Provider, Status: "validated"})
		if card.URL == "" {
			continue
		}
		card.Fingerprint = sourceFingerprint(card.URL)
		status := "extracted"
		if e.HTTPStatus < 200 || e.HTTPStatus >= 300 {
			status = "failed"
			card.ErrorCode = "fetch_failed"
		} else if strings.TrimSpace(e.Excerpt) == "" {
			status = "failed"
			card.ErrorCode = "extraction_empty"
		} else if len(e.QualityFlags) > 0 {
			status = "rejected"
			card.ErrorCode = "extraction_challenge"
		} else if cited[e.ID] {
			status = "cited"
		}
		add(card, status, e.ID, false)
	}
	return sources, nil
}
