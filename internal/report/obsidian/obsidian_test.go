package obsidian

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleJSON(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"evidence_coverage": []map[string]string{
		{"evidence_id": "3c757d00-1032-43dc-8b10-547adc82b579", "title": "SH", "url": "https://stipendiumhungaricum.hu/apply/"},
		{"evidence_id": "8e37407c-9eec-4764-a66e-0680229bba13", "title": "TB", "url": "https://www.turkiyeburslari.gov.tr/en/page/prospective-students/scholarship-coverage"},
		{"evidence_id": "1be322ed-9841-423c-ba3a-04965fc464fb", "title": "GKS", "url": "https://www.studyinkorea.go.kr/gks/2027-undergraduate.pdf"},
		{"evidence_id": "5c766154-6377-4f72-bd5d-e45213895e2b", "title": "KZ", "url": "https://www.gov.kz/memleket/entities/edu/press/news/details/sh-2025"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestBuildVaultFromReport(t *testing.T) {
	markdown, err := os.ReadFile("testdata/report.md")
	if err != nil {
		t.Fatal(err)
	}
	vault, err := Build(string(markdown), sampleJSON(t), "Гранты: IT/бакалавриат", time.Date(2026, 11, 7, 12, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}
	if vault.Folder != "2026-11-07 Гранты IT бакалавриат" {
		t.Fatalf("folder=%q", vault.Folder)
	}
	names := []string{}
	notes := map[string]string{}
	for _, note := range vault.Notes {
		names = append(names, note.Name)
		notes[note.Name] = note.Content
	}
	if strings.Join(names, "|") != "00 MOC|01 Stipendium Hungaricum|02 Türkiye Burslari|03 GKS (Корея)|Источники|Методология" {
		t.Fatalf("notes=%v", names)
	}
	moc := notes["00 MOC"]
	for _, want := range []string{"tags: [qwenchik, research, moc, index]", "created: 2026-11-07", "> [!abstract] TL;DR\n> - Самый ранний дедлайн", "[\\[1\\]](https://stipendiumhungaricum.hu/apply/)", "## Сравнение", "| Объект | мест |", "1. [[01 Stipendium Hungaricum]]", "[[Источники]]", "> [!warning] Что не найдено", "> - **MEXT**: подтверждённых сведений не найдено."} {
		if !strings.Contains(moc, want) {
			t.Fatalf("MOC lacks %q:\n%s", want, moc)
		}
	}
	section := notes["02 Türkiye Burslari"]
	for _, want := range []string{"# 02 · Türkiye Burslari", "[[00 MOC]]", "| возраст | до 21 года | [\\[2\\]](https://www.turkiyeburslari.gov.tr/en/page/prospective-students/scholarship-coverage) |", "[[01 Stipendium Hungaricum]] · [[03 GKS (Корея)]] · [[Источники]]"} {
		if !strings.Contains(section, want) {
			t.Fatalf("section note lacks %q:\n%s", want, section)
		}
	}
	for name, content := range notes {
		if strings.Contains(content, "1be322ed-9841") {
			t.Fatalf("raw evidence ID left in %s", name)
		}
	}
	if !strings.Contains(notes["Источники"], "| [\\[3\\]](https://www.studyinkorea.go.kr/gks/2027-undergraduate.pdf) | [GKS 2027") || !strings.Contains(notes["Методология"], "## Проверка") {
		t.Fatalf("sources/method notes incomplete:\n%s\n%s", notes["Источники"], notes["Методология"])
	}

	dir := t.TempDir()
	first, err := Write(dir, vault)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Write(dir, vault)
	if err != nil || filepath.Base(second) != vault.Folder+" (2)" {
		t.Fatalf("second export overwrote the first: %q %v", second, err)
	}
	if data, err := os.ReadFile(filepath.Join(first, "00 MOC.md")); err != nil || string(data) != moc {
		t.Fatalf("MOC not written: %v", err)
	}
}

func TestBuildVaultFromLegacyReport(t *testing.T) {
	legacy := "# Old\n\n## Краткое резюме\n\nАнализ основан на 1 источнике.\n\n## Факты\n\nФакт [3c757d00-1032-43dc-8b10-547adc82b579]\n\nСтатус секции: подтверждено источниками.\n\n## Источники\n\n1. SH — <https://stipendiumhungaricum.hu/apply/>\n"
	vault, err := Build(legacy, nil, "", time.Now())
	if err != nil || len(vault.Notes) != 4 || vault.Notes[1].Name != "01 Факты" {
		t.Fatalf("legacy vault=%+v err=%v", vault.Notes, err)
	}
	if _, err := Build("  ", nil, "x", time.Now()); err == nil {
		t.Fatal("empty report exported")
	}
}
