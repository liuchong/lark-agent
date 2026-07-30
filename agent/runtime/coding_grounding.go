package runtime

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/liuchong/lark-agent/agent/domain"
	errs "github.com/liuchong/lark-agent/internal/apperr"
)

var repositoryPathReferencePattern = regexp.MustCompile(
	`(?:[A-Za-z_.-][A-Za-z0-9._-]*/)+[A-Za-z_.-][A-Za-z0-9._-]*`,
)

var conventionalRootRepositoryPathPattern = regexp.MustCompile(
	`\b(?:Makefile|Dockerfile|Containerfile|go\.mod|go\.sum|Cargo\.toml|build\.zig|build\.zig\.zon)\b`,
)

var lowerCamelIdentifierPattern = regexp.MustCompile(
	`\b[a-z][a-z0-9]*(?:[A-Z][A-Za-z0-9]*)+\b`,
)

var codeIdentifierPattern = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\b`)

var concreteJSONObjectPattern = regexp.MustCompile(
	`\{\s*["'][^"']+["']\s*:`,
)

var concreteJSONArrayPattern = regexp.MustCompile(
	`\[\s*(?:\{|["']|-?[0-9]|true\b|false\b|null\b)`,
)

var serializerOperationPattern = regexp.MustCompile(
	`(?i)(?:json\.(?:marshal|dumps|stringify|encode|newencoder)|` +
		`objectmapper|gson|jsonobject|jsonarray|encodeToString|decodeFromString|` +
		`marshalJSON|unmarshalJSON)`,
)

var inlineJSONObjectPattern = regexp.MustCompile(`\{[^{}\n]*:[^{}\n]*\}`)

type structuralSearchResult struct {
	Source  domain.SourceRef `json:"source"`
	Snippet string           `json:"snippet"`
}

type structuralSearchReport struct {
	Results []structuralSearchResult `json:"results"`
}

func verifyGroundedCodingReply(
	question string,
	decision domain.Decision,
	authoritativeContents map[string]string,
) error {
	if decision.EvidenceStatus != domain.EvidenceVerified {
		return nil
	}
	citedContents := make([]string, 0, len(decision.Sources))
	readSources := make([]domain.SourceRef, 0, len(decision.Sources))
	for _, source := range decision.Sources {
		if content, ok := authoritativeContents[sourceKey(source)]; ok {
			readSources = append(readSources, source)
			citedContents = append(citedContents, content)
		}
	}
	combinedContent := strings.Join(citedContents, "\n")
	citedIdentifiers := identifierSet(combinedContent)
	replyWithoutPaths := decision.ReplyText
	pathLocations := repositoryPathReferencePattern.FindAllStringIndex(decision.ReplyText, -1)
	pathLocations = append(
		pathLocations,
		conventionalRootRepositoryPathPattern.FindAllStringIndex(decision.ReplyText, -1)...,
	)
	for _, location := range pathLocations {
		candidate := decision.ReplyText[location[0]:location[1]]
		if !looksLikeRepositoryPath(
			decision.ReplyText,
			location[0],
			candidate,
			readSources,
		) {
			continue
		}
		if !decisionCitesRepositoryPath(readSources, candidate) {
			return errs.NewInternalError(
				errs.SubtypeInvalidResponse,
				"verified coding reply contains repository path %s that is not backed by a current-run read; read and cite it or remove it",
				candidate,
			)
		}
		replyWithoutPaths = replyWithoutPaths[:location[0]] +
			strings.Repeat(" ", location[1]-location[0]) +
			replyWithoutPaths[location[1]:]
	}
	seen := make(map[string]bool)
	for _, identifier := range lowerCamelIdentifierPattern.FindAllString(
		replyWithoutPaths,
		-1,
	) {
		if seen[identifier] {
			continue
		}
		seen[identifier] = true
		if citedIdentifiers[strings.ToLower(identifier)] {
			continue
		}
		return errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"verified coding reply identifier %s is absent from all cited authoritative reads; cite supporting evidence or correct the identifier",
			identifier,
		)
	}
	if asksConcreteSerializedShape(question) {
		normalizedContents := make([]string, 0, len(citedContents))
		for _, content := range citedContents {
			normalizedContents = append(
				normalizedContents,
				normalizeEscapedSerializationText(content),
			)
		}
		if !hasStructuralSerializationEvidenceInSources(
			question,
			normalizedContents,
		) {
			return errs.NewInternalError(
				errs.SubtypeInvalidResponse,
				"verified coding reply claims a concrete serialized shape but cited reads contain only opaque declarations; read and cite a current example, protocol, or serializer",
			)
		}
		if unsupported := unsupportedConcreteJSONExample(
			normalizeEscapedSerializationText(decision.ReplyText),
			structuralSerializationEvidenceForQuestion(
				question,
				normalizedContents,
			),
		); unsupported != "" {
			return errs.NewInternalError(
				errs.SubtypeInvalidResponse,
				"verified coding reply contains unsupported serialized example %s; cite the exact current-run evidence or correct it",
				unsupported,
			)
		}
	}
	return nil
}

func looksLikeRepositoryPath(
	text string,
	start int,
	candidate string,
	readSources []domain.SourceRef,
) bool {
	if repositoryPathIsURL(text, start) ||
		(start > 0 && text[start-1] == '/') {
		return false
	}
	if conventionalRootRepositoryPathPattern.MatchString(candidate) ||
		strings.Contains(filepathBase(candidate), ".") {
		return true
	}
	candidateRoot := repositoryPathRoot(candidate)
	if candidateRoot == "" {
		return false
	}
	for _, source := range readSources {
		if strings.EqualFold(
			repositoryPathRoot(source.RelativePath),
			candidateRoot,
		) {
			return true
		}
	}
	return false
}

func filepathBase(path string) string {
	if index := strings.LastIndex(path, "/"); index >= 0 {
		return path[index+1:]
	}
	return path
}

func repositoryPathRoot(path string) string {
	path = strings.TrimPrefix(path, "./")
	if index := strings.Index(path, "/"); index >= 0 {
		return path[:index]
	}
	return ""
}

func identifierSet(content string) map[string]bool {
	identifiers := make(map[string]bool)
	for _, identifier := range codeIdentifierPattern.FindAllString(content, -1) {
		identifiers[strings.ToLower(identifier)] = true
	}
	return identifiers
}

func asksConcreteSerializedShape(question string) bool {
	question = strings.ToLower(question)
	hasShapeIntent := containsAny(
		question,
		"结构",
		"格式",
		"形状",
		"长什么样",
		"怎么传",
		"如何传",
		"具体内容",
		"具体值",
		"shape",
		"schema",
		"format",
	)
	hasSerializedTarget := containsAny(
		question,
		"json",
		"序列化",
		"字符串",
		"字节",
		"请求体",
		"payload",
		"body",
		"string",
		"bytes",
		"rawmessage",
		"samplecontent",
	)
	return hasShapeIntent && hasSerializedTarget
}

func normalizeEscapedSerializationText(content string) string {
	return strings.ReplaceAll(content, `\"`, `"`)
}

func hasStructuralSerializationEvidence(content string) bool {
	return concreteJSONObjectPattern.MatchString(content) ||
		concreteJSONArrayPattern.MatchString(content) ||
		serializerOperationPattern.MatchString(content)
}

func hasStructuralSerializationEvidenceForQuestion(
	question string,
	content string,
) bool {
	if !hasStructuralSerializationEvidence(content) {
		return false
	}
	targets := concreteSerializedShapeTargets(question)
	if len(targets) == 0 {
		return true
	}
	for _, target := range targets {
		if len(structuralSerializationEvidenceContexts(content, target)) == 0 {
			return false
		}
	}
	return true
}

func structuralSerializationEvidenceForQuestion(
	question string,
	contents []string,
) string {
	targets := concreteSerializedShapeTargets(question)
	if len(targets) == 0 {
		return strings.Join(contents, "\n")
	}
	contexts := make([]string, 0, len(contents))
	for _, content := range contents {
		for _, target := range targets {
			contexts = append(
				contexts,
				structuralSerializationEvidenceContexts(content, target)...,
			)
		}
	}
	return strings.Join(contexts, "\n")
}

func structuralSerializationEvidenceContexts(
	content string,
	target string,
) []string {
	lines := strings.Split(content, "\n")
	contexts := make([]string, 0, 1)
	for index, line := range lines {
		if !identifierSet(line)[strings.ToLower(target)] {
			continue
		}
		if lineDeniesStructuralSerializationEvidence(line) {
			continue
		}
		if hasStructuralSerializationEvidence(line) {
			contexts = append(contexts, line)
			continue
		}
		if !lineIntroducesStructuralSerializationEvidence(line) {
			continue
		}
		end := min(len(lines), index+5)
		for followingIndex := index + 1; followingIndex < end; followingIndex++ {
			followingLine := lines[followingIndex]
			if lineIntroducesStructuralSerializationEvidence(followingLine) &&
				lineNamesDifferentStructuralTarget(followingLine, target) {
				break
			}
			if !hasStructuralSerializationEvidence(followingLine) {
				continue
			}
			if lineNamesDifferentStructuralTarget(followingLine, target) {
				break
			}
			contexts = append(
				contexts,
				strings.Join(lines[index:followingIndex+1], "\n"),
			)
			break
		}
	}
	return contexts
}

func hasStructuralSerializationEvidenceInSources(
	question string,
	contents []string,
) bool {
	for _, content := range contents {
		if hasStructuralSerializationEvidenceForQuestion(question, content) {
			return true
		}
	}
	return false
}

func lineIntroducesStructuralSerializationEvidence(line string) bool {
	line = strings.ToLower(line)
	return containsAny(
		line,
		"json",
		"结构",
		"格式",
		"形状",
		"示例",
		"样例",
		"example",
		"schema",
		"payload",
		"request value",
		"请求值",
	)
}

func lineDeniesStructuralSerializationEvidence(line string) bool {
	line = strings.ToLower(line)
	return containsAny(
		line,
		"未定义",
		"未知",
		"没有定义",
		"无定义",
		"不在此处",
		"无法提供",
		"not defined",
		"undefined",
		"unknown",
		"unavailable",
		"not available",
		"absent",
		"missing",
	)
}

func lineNamesDifferentStructuralTarget(line, target string) bool {
	for _, identifier := range lowerCamelIdentifierPattern.FindAllString(line, -1) {
		if !strings.EqualFold(identifier, target) {
			return true
		}
	}
	return false
}

func concreteSerializedShapeTargets(question string) []string {
	locations := lowerCamelIdentifierPattern.FindAllStringIndex(question, -1)
	if len(locations) == 0 {
		return nil
	}
	anchors := serializationShapeAnchorLocations(strings.ToLower(question))
	if len(anchors) == 0 {
		return nil
	}
	bestDistance := len(question) + 1
	distances := make([]int, len(locations))
	for index, location := range locations {
		distance := len(question) + 1
		for _, anchor := range anchors {
			distance = min(distance, intervalDistance(location, anchor))
		}
		distances[index] = distance
		bestDistance = min(bestDistance, distance)
	}
	targets := make([]string, 0, len(locations))
	seen := make(map[string]bool)
	for index, location := range locations {
		if distances[index] > bestDistance+24 {
			continue
		}
		target := question[location[0]:location[1]]
		key := strings.ToLower(target)
		if seen[key] {
			continue
		}
		seen[key] = true
		targets = append(targets, target)
	}
	return targets
}

func serializationShapeAnchorLocations(question string) [][2]int {
	anchors := make([][2]int, 0, 8)
	for _, marker := range []string{
		"结构", "格式", "形状", "长什么样", "怎么传", "如何传",
		"具体内容", "具体值", "json", "序列化", "字符串", "字节",
		"请求体", "payload", "body", "string", "bytes", "rawmessage",
	} {
		offset := 0
		for {
			index := strings.Index(question[offset:], marker)
			if index < 0 {
				break
			}
			start := offset + index
			anchors = append(anchors, [2]int{start, start + len(marker)})
			offset = start + len(marker)
		}
	}
	return anchors
}

func intervalDistance(left []int, right [2]int) int {
	if left[1] < right[0] {
		return right[0] - left[1]
	}
	if right[1] < left[0] {
		return left[0] - right[1]
	}
	return 0
}

func missingStructuralSerializationEvidence(
	question string,
	authoritativeContents map[string]string,
) bool {
	if !asksConcreteSerializedShape(question) {
		return false
	}
	contents := make([]string, 0, len(authoritativeContents))
	for _, content := range authoritativeContents {
		contents = append(
			contents,
			normalizeEscapedSerializationText(content),
		)
	}
	return !hasStructuralSerializationEvidenceInSources(
		question,
		contents,
	)
}

func hasOpaqueSerializationDeclarationForQuestion(
	question string,
	authoritativeContents map[string]string,
) bool {
	targets := concreteSerializedShapeTargets(question)
	if len(targets) == 0 {
		return false
	}
	for _, content := range authoritativeContents {
		for _, line := range strings.Split(content, "\n") {
			lineIdentifiers := identifierSet(line)
			lineLower := strings.ToLower(line)
			for _, target := range targets {
				if lineIdentifiers[strings.ToLower(target)] &&
					containsAny(
						lineLower,
						"string",
						"[]byte",
						"byte[]",
						"bytes",
						"rawmessage",
						"json.rawmessage",
					) {
					return true
				}
			}
		}
	}
	return false
}

func structuralEvidenceSearchQuery(question string) string {
	targets := concreteSerializedShapeTargets(question)
	if len(targets) == 0 {
		return ""
	}
	return targets[0]
}

func structuralEvidenceSearchRecoveryPrompt(query string) string {
	return fmt.Sprintf(
		"The current reads prove only an opaque declaration for %s. "+
			"Use exactly one exact field-name search_workspace call in this turn, with query exactly %q, "+
			"inside the existing exact repository scope and omit max_results. Do not read another production "+
			"symbol and do not submit an insufficient decision yet. The runtime will "+
			"filter the result snippets to field-related structural candidates for the next turn.",
		query,
		query,
	)
}

func structuralEvidenceReadRecoveryPrompt(candidates []string) string {
	candidates = append([]string(nil), candidates...)
	sort.Strings(candidates)
	return "The bounded field-name search found structural evidence candidates. " +
		"Use exactly one read_workspace call in this turn and read one of these exact paths: " +
		strings.Join(candidates, ", ") +
		". Other paths, search, listing, and submit_decision are unavailable until this read completes."
}

func validateStructuralEvidenceSearchArguments(
	question string,
	arguments string,
	requestedWorkspaceScope string,
) error {
	var input struct {
		Query      string `json:"query"`
		Path       string `json:"path"`
		MaxResults *int   `json:"max_results"`
	}
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"decode structural-evidence search arguments: %v",
			err,
		)
	}
	required := structuralEvidenceSearchQuery(question)
	if required == "" || strings.TrimSpace(input.Query) != required {
		return errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"structural-evidence recovery requires search_workspace query %q",
			required,
		)
	}
	scope := strings.Trim(strings.TrimSpace(requestedWorkspaceScope), "/")
	searchPath := strings.Trim(strings.TrimSpace(input.Path), "/")
	if searchPath != "" && searchPath != scope {
		return errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"structural-evidence recovery must search the exact repository scope %q; path %q narrows or changes that scope",
			scope,
			input.Path,
		)
	}
	if input.MaxResults != nil {
		return errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"structural-evidence recovery must not set max_results; the runtime controls the bounded full-scope result limit",
		)
	}
	return nil
}

func structuralEvidenceCandidatePaths(question, output string) []string {
	var report structuralSearchReport
	if json.Unmarshal([]byte(output), &report) != nil {
		return nil
	}
	seen := make(map[string]bool)
	candidates := make([]string, 0, len(report.Results))
	for _, result := range report.Results {
		path := strings.TrimSpace(result.Source.RelativePath)
		snippet := normalizeEscapedSerializationText(result.Snippet)
		if path == "" ||
			seen[path] ||
			!hasStructuralSerializationEvidenceForQuestion(question, snippet) {
			continue
		}
		seen[path] = true
		candidates = append(candidates, path)
	}
	sort.Strings(candidates)
	return candidates
}

func validateStructuralEvidenceReadArguments(
	arguments string,
	requestedWorkspaceScope string,
	candidates []string,
) error {
	var input struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return errs.NewInternalError(
			errs.SubtypeInvalidResponse,
			"decode structural-evidence read arguments: %v",
			err,
		)
	}
	path := strings.Trim(strings.TrimSpace(input.Path), "/")
	scope := strings.Trim(strings.TrimSpace(requestedWorkspaceScope), "/")
	for _, candidate := range candidates {
		candidate = strings.Trim(strings.TrimSpace(candidate), "/")
		if path == candidate ||
			(scope != "" && scope+"/"+path == candidate) {
			return nil
		}
	}
	return errs.NewInternalError(
		errs.SubtypeInvalidResponse,
		"structural-evidence recovery read path %q is not one of the field-related search candidates",
		input.Path,
	)
}

func unsupportedConcreteJSONExample(reply, evidence string) string {
	compactEvidence := compactSerializationExample(evidence)
	for _, example := range inlineJSONObjectPattern.FindAllString(reply, -1) {
		compactExample := compactSerializationExample(example)
		if compactExample != "" && !strings.Contains(compactEvidence, compactExample) {
			return example
		}
	}
	return ""
}

func compactSerializationExample(value string) string {
	return strings.Join(strings.Fields(value), "")
}

func repositoryPathIsURL(text string, start int) bool {
	prefixStart := start - len("https://")
	if prefixStart < 0 {
		prefixStart = 0
	}
	prefix := strings.ToLower(text[prefixStart:start])
	return strings.HasSuffix(prefix, "http://") ||
		strings.HasSuffix(prefix, "https://")
}

func decisionCitesRepositoryPath(
	sources []domain.SourceRef,
	candidate string,
) bool {
	candidate = strings.TrimPrefix(candidate, "./")
	for _, source := range sources {
		sourcePath := strings.TrimPrefix(source.RelativePath, "./")
		if sourcePath == candidate ||
			strings.HasSuffix(sourcePath, "/"+candidate) {
			return true
		}
	}
	return false
}
