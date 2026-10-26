package orchestrator

import (
	"context"
	"fmt"
	"mime"
	"sort"
	"strings"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

// Notes are extractive records, not model-authored provenance or verified truth.
// Raw documents remain in the existing artifact store and never enter this view.
func buildSourceNotes(taskID string, ids []string, evidence []domain.Evidence, artifacts []domain.Artifact, coverage []domain.VerificationCriterionResult, missing []string) ([]domain.SourceNote, error) {
	if len(ids) > 32 {
		return nil, errInputBudgetExceeded
	}
	owned := map[string]domain.Evidence{}
	for _, item := range evidence {
		if item.TaskID == taskID {
			owned[item.ID] = item
		}
	}
	files := map[string]domain.Artifact{}
	for _, item := range artifacts {
		if item.TaskID == taskID {
			files[item.ID] = item
		}
	}
	notes := []domain.SourceNote{}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return nil, sqlite.ErrCheckpointInvalid
		}
		seen[id] = true
		item, exists := owned[id]
		if !exists {
			return nil, sqlite.ErrCheckpointInvalid
		}
		if item.HTTPStatus < 200 || item.HTTPStatus >= 300 || len(item.QualityFlags) > 0 || strings.TrimSpace(item.Excerpt) == "" {
			continue
		}
		raw, rawOK := files[item.RawArtifactID]
		text, textOK := files[item.TextArtifactID]
		textMIME, _, mimeErr := mime.ParseMediaType(text.MIMEType)
		if !rawOK || !textOK || raw.SHA256 == "" || raw.SHA256 != item.ContentSHA256 || text.SHA256 == "" || mimeErr != nil || textMIME != "text/plain" {
			return nil, sqlite.ErrCheckpointInvalid
		}
		excerpt := truncateRunes(item.Excerpt, 1500)
		note := domain.SourceNote{Version: 1, EvidenceID: id, CanonicalURL: item.CanonicalURL, Title: truncateRunes(item.Title, 160), SourceType: raw.MIMEType, RetrievedAt: item.RetrievedAt.UTC().Format(time.RFC3339Nano), ContentHash: raw.SHA256, ExtractedTextHash: text.SHA256, RelevantExcerpts: []string{excerpt}, Claims: []string{excerpt}, UnresolvedCriteria: append([]string{}, missing...), QualityFlags: append([]string{}, item.QualityFlags...), SourceFailureState: "accepted", Taint: "untrusted_external_content"}
		for _, criterion := range coverage {
			if criterion.Met && contains(criterion.EvidenceIDs, id) {
				note.SupportedCriteria = append(note.SupportedCriteria, fmt.Sprintf("%s.%d", criterion.StepID, criterion.Criterion))
			}
		}
		notes = append(notes, note)
	}
	sort.Slice(notes, func(i, j int) bool { return notes[i].EvidenceID < notes[j].EvidenceID })
	return notes, nil
}

func (w *Worker) refreshSourceNotes(ctx context.Context, task domain.Task, state *checkpointState) error {
	session, ok := ctx.Value(researchExecutionKey{}).(domain.ResearchSession)
	if !ok || (!session.Policy.AdaptiveEnabled && !session.Policy.SectionedReportEnabled) {
		return nil
	}
	evidence, err := w.repo.Evidence(ctx, task.ID)
	if err != nil {
		return err
	}
	artifacts, err := w.repo.Artifacts(ctx, task.ID)
	if err != nil {
		return err
	}
	var coverage []domain.VerificationCriterionResult
	var missing []string
	if state.Adaptive != nil && len(state.Adaptive.Rounds) > 0 {
		round := state.Adaptive.Rounds[len(state.Adaptive.Rounds)-1]
		coverage, missing = round.Coverage, round.CriticalMissing
	}
	state.SourceNotes, err = buildSourceNotes(task.ID, collectEvidenceIDs(state.ToolResults), evidence, artifacts, coverage, missing)
	if err != nil {
		return err
	}
	_, err = w.repo.SaveCheckpoint(ctx, task.ID, 1, *state)
	return err
}

func compactSourceNotes(notes []domain.SourceNote) []domain.SourceNote {
	selected := []domain.SourceNote{}
	for _, note := range notes[:min(8, len(notes))] {
		note.RelevantExcerpts = append([]string{}, note.RelevantExcerpts...)
		for index := range note.RelevantExcerpts {
			note.RelevantExcerpts[index] = truncateRunes(note.RelevantExcerpts[index], 1000)
		}
		note.Claims = append([]string{}, note.RelevantExcerpts...)
		selected = append(selected, note)
	}
	return selected
}
