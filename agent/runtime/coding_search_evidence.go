package runtime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	agentcontext "github.com/liuchong/lark-agent/agent/context"
	agentlocale "github.com/liuchong/lark-agent/agent/locale"
)

type codingSearchObservation struct {
	Query              string
	Matches            int
	Truncated          bool
	FilesScanned       int
	DirectoriesScanned int
}

type codingSearchEvidence struct {
	observations []codingSearchObservation
	receiptCount int
	unparseable  bool
	sawMatch     bool
}

type codingSearchReport struct {
	Results            []json.RawMessage `json:"results"`
	Truncated          bool              `json:"truncated"`
	FilesScanned       int               `json:"files_scanned"`
	DirectoriesScanned int               `json:"directories_scanned"`
}

const maxRenderedCodingSearchObservations = 16

func (e *codingSearchEvidence) Record(toolName, inputJSON, outputJSON string) {
	switch toolName {
	case "search_workspace":
		var input struct {
			Query string `json:"query"`
		}
		var report codingSearchReport
		if json.Unmarshal([]byte(inputJSON), &input) != nil ||
			!decodeCodingSearchReport([]byte(outputJSON), &report) ||
			strings.TrimSpace(input.Query) == "" {
			e.unparseable = true
			return
		}
		e.add(codingSearchObservation{
			Query:              input.Query,
			Matches:            len(report.Results),
			Truncated:          report.Truncated,
			FilesScanned:       report.FilesScanned,
			DirectoriesScanned: report.DirectoriesScanned,
		})
	case "search_code_symbols":
		var report struct {
			Query    string          `json:"query"`
			Results  json.RawMessage `json:"results"`
			Fallback json.RawMessage `json:"fallback"`
		}
		if json.Unmarshal([]byte(outputJSON), &report) != nil ||
			strings.TrimSpace(report.Query) == "" {
			e.unparseable = true
			return
		}
		if len(report.Fallback) > 0 && string(report.Fallback) != "null" {
			var fallback codingSearchReport
			if !decodeCodingSearchReport(report.Fallback, &fallback) {
				e.unparseable = true
				return
			}
			e.add(codingSearchObservation{
				Query:              report.Query,
				Matches:            len(fallback.Results),
				Truncated:          fallback.Truncated,
				FilesScanned:       fallback.FilesScanned,
				DirectoriesScanned: fallback.DirectoriesScanned,
			})
			return
		}
		var results []json.RawMessage
		if len(report.Results) == 0 || json.Unmarshal(report.Results, &results) != nil {
			e.unparseable = true
			return
		}
		if len(results) == 0 {
			// A direct code-index miss has no bounded workspace scan receipt,
			// so it cannot support a receipt-backed negative reply.
			e.unparseable = true
			return
		}
		e.add(codingSearchObservation{
			Query:   report.Query,
			Matches: len(results),
		})
	case "explore_workspace":
		var report struct {
			Queries []json.RawMessage `json:"queries"`
		}
		if json.Unmarshal([]byte(outputJSON), &report) != nil || len(report.Queries) == 0 {
			e.unparseable = true
			return
		}
		for _, rawQuery := range report.Queries {
			var query struct {
				Query string `json:"query"`
				codingSearchReport
			}
			if json.Unmarshal(rawQuery, &query) != nil ||
				!decodeCodingSearchReport(rawQuery, &query.codingSearchReport) {
				e.unparseable = true
				return
			}
			if strings.TrimSpace(query.Query) == "" {
				e.unparseable = true
				return
			}
			e.add(codingSearchObservation{
				Query:              query.Query,
				Matches:            len(query.Results),
				Truncated:          query.Truncated,
				FilesScanned:       query.FilesScanned,
				DirectoriesScanned: query.DirectoriesScanned,
			})
		}
	}
}

func decodeCodingSearchReport(raw []byte, report *codingSearchReport) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for _, required := range []string{
		"results",
		"truncated",
		"files_scanned",
		"directories_scanned",
	} {
		if _, ok := fields[required]; !ok {
			return false
		}
	}
	for _, metadata := range []string{
		"truncated",
		"files_scanned",
		"directories_scanned",
	} {
		if bytes.Equal(bytes.TrimSpace(fields[metadata]), []byte("null")) {
			return false
		}
	}
	if json.Unmarshal(raw, report) != nil {
		return false
	}
	return report.FilesScanned >= 0 && report.DirectoriesScanned >= 0
}

func (e *codingSearchEvidence) add(observation codingSearchObservation) {
	observation.Query = strings.TrimSpace(observation.Query)
	if observation.Query == "" {
		e.unparseable = true
		return
	}
	e.receiptCount++
	if observation.Matches > 0 {
		e.sawMatch = true
	}
	key := fmt.Sprintf(
		"%s\x00%d\x00%t\x00%d\x00%d",
		observation.Query,
		observation.Matches,
		observation.Truncated,
		observation.FilesScanned,
		observation.DirectoriesScanned,
	)
	for _, existing := range e.observations {
		existingKey := fmt.Sprintf(
			"%s\x00%d\x00%t\x00%d\x00%d",
			existing.Query,
			existing.Matches,
			existing.Truncated,
			existing.FilesScanned,
			existing.DirectoriesScanned,
		)
		if existingKey == key {
			return
		}
	}
	e.observations = append(e.observations, observation)
}

func (e codingSearchEvidence) canRenderNegativeResult() bool {
	return !e.unparseable &&
		!e.sawMatch &&
		len(e.observations) > 0 &&
		e.receiptCount <= maxRenderedCodingSearchObservations
}

func (e codingSearchEvidence) renderNegativeResult(bundle agentcontext.Bundle) string {
	if resolvedBundleLanguage(bundle) == agentlocale.LanguageEnglish {
		var lines []string
		for _, observation := range e.observations {
			lines = append(lines, "- "+renderEnglishSearchObservation(observation))
		}
		return "Conclusion: no match was found within these bounded checks; this does not prove that the symbol cannot exist outside the scanned scope.\n" +
			"Evidence: every check stayed inside the configured Workspace:\n" +
			strings.Join(lines, "\n") + "\n" +
			"Unknown/next step: narrow the search to a named repository or directory if exhaustive confirmation is required."
	}
	var lines []string
	for _, observation := range e.observations {
		lines = append(lines, "- "+renderChineseSearchObservation(observation))
	}
	return "结论：在本次有界检查范围内没有找到匹配项；这不等于证明它在未扫描范围中绝对不存在。\n" +
		"依据：以下检查均限定在配置的 Workspace 内：\n" +
		strings.Join(lines, "\n") + "\n" +
		"未知/下一步：如需穷尽确认，请再指定具体仓库或子目录做定向核对。"
}

func renderChineseSearchObservation(observation codingSearchObservation) string {
	query := compactSearchQuery(observation.Query)
	scan := "扫描数量未提供"
	if observation.FilesScanned > 0 || observation.DirectoriesScanned > 0 {
		scan = fmt.Sprintf(
			"扫描了 %d 个文件、%d 个目录",
			observation.FilesScanned,
			observation.DirectoriesScanned,
		)
	}
	state := "扫描完整"
	if observation.Truncated {
		state = "扫描达到工具上限"
	}
	return fmt.Sprintf("查询“%s”：0 条匹配，%s，%s。", query, scan, state)
}

func renderEnglishSearchObservation(observation codingSearchObservation) string {
	query := compactSearchQuery(observation.Query)
	scan := "scan counts were not reported"
	if observation.FilesScanned > 0 || observation.DirectoriesScanned > 0 {
		scan = fmt.Sprintf(
			"scanned %d files and %d directories",
			observation.FilesScanned,
			observation.DirectoriesScanned,
		)
	}
	state := "the bounded scan completed"
	if observation.Truncated {
		state = "the scan reached its tool limit"
	}
	return fmt.Sprintf("query %q: 0 matches; %s; %s.", query, scan, state)
}

func compactSearchQuery(query string) string {
	query = strings.Join(strings.Fields(query), " ")
	const maxRunes = 120
	runes := []rune(query)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "..."
	}
	return query
}
