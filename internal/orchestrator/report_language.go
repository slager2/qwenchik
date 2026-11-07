package orchestrator

import (
	"fmt"
	"unicode"
)

// reportLanguage picks the language of model-written and backend report
// text from the research question. A question that is mostly Cyrillic is
// Russian; anything else follows the question as written, with English
// backend labels.
func reportLanguage(question string) string {
	cyrillic, latin := 0, 0
	for _, r := range question {
		switch {
		case unicode.Is(unicode.Cyrillic, r):
			cyrillic++
		case unicode.Is(unicode.Latin, r):
			latin++
		}
	}
	// Goals may carry short English wrappers ("Original task:") and English
	// technical terms; a Russian question still dominates them.
	if cyrillic >= 3 && cyrillic*2 >= latin {
		return "ru"
	}
	return "en"
}

type reportLabels struct {
	Language          string // instruction fragment for the model
	WriterExample     string
	DefaultSection    string
	CrossSection      string // cross-cutting section of an entity outline
	Summary           string // executive summary format: source count
	SummaryHeading    string
	QuestionHeading   string
	ContentsHeading   string
	LimitsHeading     string
	LimitsText        string
	SupplementaryItem string
	SourceUnused      string // title, code, bytes
	Unrepresented     string
	MethodHeading     string
	MethodText        string
	PassageMethod     string // added when a source is agentSearcher passages
	PassageSource     string // source-list note for agentSearcher passages
	ExtractMethod     string // added when a source was read by agentSearcher /extract
	PDFSource         string // source-list note for PDF sources
	OfflineText       string
	SourcesHeading    string
	CoverageHeading   string
	CoverageText      string // sources, sections, missing, outcome
	SectionStatus     string
	SectionLimit      string
	Statuses          map[string]string
	Limitations       map[string]string
}

var reportLabelsRU = reportLabels{
	Language:          "Write in Russian, even when the sources are in another language.",
	WriterExample:     "Первый факт из источника 1 [1].\n- Факт из источника 2 [2].\n- Факт, который подтверждают источники 1 и 3 [1][3].",
	DefaultSection:    "Подтверждённые сведения",
	CrossSection:      "Общие условия, расходы и шаги",
	Summary:           "Анализ основан на %d сохранённых источниках. Каждое утверждение проверено по выдержке из указанного источника; статусы секций и ограничения указаны явно.",
	SummaryHeading:    "Краткое резюме",
	QuestionHeading:   "Вопрос исследования",
	ContentsHeading:   "Содержание",
	LimitsHeading:     "Ограничения",
	LimitsText:        "Проверка той же локальной моделью не является независимым доказательством фактической истинности. Объём отчёта ограничен доступными выдержками и бюджетом.",
	SupplementaryItem: "Дополнительный критерий не подтверждён: %s",
	SourceUnused:      "Источник не использован: %s (%s); прочитано %d байт.",
	Unrepresented:     "Секционное покрытие не подтверждено для критериев: %s",
	MethodHeading:     "Методология",
	MethodText:        "Источники загружены и извлечены backend. Каждое утверждение проверено по точной выдержке из процитированного источника; неподтверждённые утверждения удалены, а секции без подтверждённых утверждений заменены выдержками. Список источников собран кодом.",
	PassageMethod:     "Некоторые сайты заблокировали прямую загрузку; для них использованы выдержки страницы, которые прочитал поисковый сервис agentSearcher. Такие источники отмечены в списке.",
	PassageSource:     "текст из выдержек agentSearcher, прямая загрузка не удалась",
	ExtractMethod:     "Часть страниц и PDF загружена через локальный сервис agentSearcher (загрузка с браузерным отпечатком, при необходимости headless-браузер, извлечение текста PDF); когда он не справлялся, использовалась прямая загрузка backend.",
	PDFSource:         "PDF",
	OfflineText:       "Использованы только импортированные тексты. Corpus SHA-256: `%s`. Актуальность источников в сети не проверялась.",
	SourcesHeading:    "Источники",
	CoverageHeading:   "Покрытие",
	CoverageText:      "Источников: %d. Секций: %d. Неподтверждённых критических критериев: %d. Итог: %s.",
	SectionStatus:     "Статус секции: %s.",
	SectionLimit:      "Ограничение секции: %s.",
	Statuses:          map[string]string{"supported": "подтверждено источниками", "fallback_excerpts": "только выдержки из источников"},
	Limitations: map[string]string{
		"unsupported_claims_removed": "часть утверждений не подтвердилась выдержками и удалена",
		"report_circuit_breaker":     "повторяющаяся ошибка модели, секция заменена выдержками",
		"claim_not_supported":        "ни одно утверждение не подтвердилось выдержками",
		"citation_mapping_invalid":   "черновик не прошёл проверку формата ссылок",
		"invalid_model_output":       "модель вернула некорректный ответ",
		"output_truncated":           "ответ модели был обрезан",
		"evidence_unavailable":       "нет доступных выдержек",
		"input_budget_exceeded":      "входные данные не поместились в контекст модели",
		"budget_exhausted":           "исчерпан бюджет исследования",
	},
}

var reportLabelsEN = reportLabels{
	Language:          "Write in the language of the research question.",
	WriterExample:     "First fact from source 1 [1].\n- A fact from source 2 [2].\n- A fact stated by sources 1 and 3 [1][3].",
	DefaultSection:    "Verified findings",
	CrossSection:      "Costs, conditions and next steps",
	Summary:           "The analysis is based on %d stored sources. Every statement was checked against an excerpt of the cited source; section statuses and limitations are stated explicitly.",
	SummaryHeading:    "Summary",
	QuestionHeading:   "Research question",
	ContentsHeading:   "Contents",
	LimitsHeading:     "Limitations",
	LimitsText:        "Verification by the same local model is not independent proof of factual truth. The report is limited to the available excerpts and budget.",
	SupplementaryItem: "Supplementary criterion not confirmed: %s",
	SourceUnused:      "Source not used: %s (%s); read %d bytes.",
	Unrepresented:     "Section coverage not confirmed for criteria: %s",
	MethodHeading:     "Methodology",
	MethodText:        "Sources were fetched and extracted by the backend. Each statement was checked against an exact excerpt of its cited source; unsupported statements were removed and sections without supported statements were replaced with excerpts. The source list is assembled by code.",
	PassageMethod:     "Some sites blocked the direct fetch; for those, the page passages read by the agentSearcher search service were used. Such sources are marked in the list.",
	PassageSource:     "text from agentSearcher passages, direct fetch failed",
	ExtractMethod:     "Some pages and PDFs were read through the local agentSearcher service (browser-like download, a headless browser when needed, PDF text extraction); when it failed, the backend fetched the page directly.",
	PDFSource:         "PDF",
	OfflineText:       "Only imported texts were used. Corpus SHA-256: `%s`. Online currency of the sources was not checked.",
	SourcesHeading:    "Sources",
	CoverageHeading:   "Coverage",
	CoverageText:      "Sources: %d. Sections: %d. Unconfirmed critical criteria: %d. Outcome: %s.",
	SectionStatus:     "Section status: %s.",
	SectionLimit:      "Section limitation: %s.",
	Statuses:          map[string]string{"supported": "supported by sources", "fallback_excerpts": "source excerpts only"},
	Limitations: map[string]string{
		"unsupported_claims_removed": "some statements were not supported by the excerpts and were removed",
		"report_circuit_breaker":     "repeated model failure, section replaced with excerpts",
		"claim_not_supported":        "no statement was supported by the excerpts",
		"citation_mapping_invalid":   "the draft failed citation format checks",
		"invalid_model_output":       "the model returned an invalid answer",
		"output_truncated":           "the model answer was truncated",
		"evidence_unavailable":       "no excerpts available",
		"input_budget_exceeded":      "the input did not fit the model context",
		"budget_exhausted":           "the research budget was exhausted",
	},
}

func reportLabelsFor(question string) reportLabels {
	if reportLanguage(question) == "ru" {
		return reportLabelsRU
	}
	return reportLabelsEN
}

func (l reportLabels) status(code string) string {
	if text, ok := l.Statuses[code]; ok {
		return text
	}
	return code
}

func (l reportLabels) limitation(code string) string {
	if text, ok := l.Limitations[code]; ok {
		return text
	}
	return code
}

func (l reportLabels) summary(sources int) string {
	return fmt.Sprintf(l.Summary, sources)
}
