package orchestrator

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// evidenceAliases maps task-owned evidence UUIDs to short stable aliases
// (E1, E2, ...) for model-facing role inputs. A UUID costs ~22 tokens each
// time the model copies it; the verifier copies one or two per criterion, so
// aliases cut its generated tokens by roughly a third on a 4B local model.
// Aliases never leave the role: outputs are mapped back before validation.
type evidenceAliases struct {
	toAlias map[string]string
	toID    map[string]string
	ids     []string
}

func newEvidenceAliases(ids ...[]string) evidenceAliases {
	set := map[string]struct{}{}
	for _, group := range ids {
		for _, id := range group {
			if id != "" {
				set[id] = struct{}{}
			}
		}
	}
	sorted := make([]string, 0, len(set))
	for id := range set {
		sorted = append(sorted, id)
	}
	sort.Strings(sorted) // deterministic, so a resumed role rebuilds the same aliases
	aliases := evidenceAliases{toAlias: make(map[string]string, len(sorted)), toID: make(map[string]string, len(sorted)), ids: sorted}
	for index, id := range sorted {
		alias := fmt.Sprintf("E%d", index+1)
		aliases.toAlias[id], aliases.toID[alias] = alias, id
	}
	return aliases
}

// apply replaces every evidence UUID occurrence in an encoded role input.
// Longer IDs are replaced first so one ID can never corrupt another.
func (a evidenceAliases) apply(input string) string {
	ids := append([]string(nil), a.ids...)
	sort.SliceStable(ids, func(i, j int) bool { return len(ids[i]) > len(ids[j]) })
	pairs := make([]string, 0, 2*len(ids))
	for _, id := range ids {
		pairs = append(pairs, id, a.toAlias[id])
	}
	if len(pairs) == 0 {
		return input
	}
	return strings.NewReplacer(pairs...).Replace(input)
}

// mentionsOriginal reports whether an (e.g. pinned, pre-alias) input still
// contains raw evidence IDs, in which case the role must not use aliases.
func (a evidenceAliases) mentionsOriginal(input string) bool {
	for _, id := range a.ids {
		if strings.Contains(input, id) {
			return true
		}
	}
	return false
}

func (a evidenceAliases) aliasList(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		if alias, ok := a.toAlias[id]; ok {
			out[i] = alias
		} else {
			out[i] = id
		}
	}
	return out
}

func (a evidenceAliases) resolveList(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = a.resolve(id)
	}
	return out
}

func (a evidenceAliases) aliasCatalog(catalog []verificationCriterion) []verificationCriterion {
	out := make([]verificationCriterion, len(catalog))
	for i, criterion := range catalog {
		criterion.EvidenceIDs = a.aliasList(criterion.EvidenceIDs)
		out[i] = criterion
	}
	return out
}

// newNumberedEvidenceAliases numbers IDs 1..n in the given order. The report
// uses the evidence catalogue order, which is also the order of the rendered
// source list, so a model-facing [2] always means the second listed source.
func newNumberedEvidenceAliases(ids []string) evidenceAliases {
	aliases := evidenceAliases{toAlias: map[string]string{}, toID: map[string]string{}}
	for _, id := range ids {
		if id == "" || aliases.toAlias[id] != "" {
			continue
		}
		alias := strconv.Itoa(len(aliases.ids) + 1)
		aliases.ids = append(aliases.ids, id)
		aliases.toAlias[id], aliases.toID[alias] = alias, id
	}
	return aliases
}

// resolve maps an alias back to its evidence ID. A small model sometimes wraps
// the alias (`[2]`, `"E2"`, `{"evidence_id": "2"}`); when exactly one known
// alias or original ID occurs in such a value it is used, otherwise the value
// is returned unchanged and later validation rejects it.
func (a evidenceAliases) resolve(id string) string {
	trimmed := strings.TrimSpace(id)
	if original, ok := a.toID[trimmed]; ok {
		return original
	}
	if _, ok := a.toAlias[trimmed]; ok {
		return trimmed
	}
	found := ""
	for _, token := range aliasTokenPattern.FindAllString(trimmed, -1) {
		candidate, ok := a.toID[token]
		if !ok {
			if _, known := a.toAlias[token]; !known {
				continue
			}
			candidate = token
		}
		if found != "" && found != candidate {
			return id
		}
		found = candidate
	}
	if found != "" {
		return found
	}
	return id
}

// aliasTokenPattern matches alias-like tokens (2, E2) and UUIDs.
var aliasTokenPattern = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|E?[0-9]+`)
