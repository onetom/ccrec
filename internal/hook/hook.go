package hook

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/capybara-translation/ccrec/internal/formatter"
	"github.com/capybara-translation/ccrec/internal/parser"
	"github.com/capybara-translation/ccrec/internal/safefile"
)

// HookInput represents the JSON passed via stdin from Claude Code or Codex hooks.
type HookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	StopHookActive bool   `json:"stop_hook_active"`
	// LastAssistantMessage is the final assistant reply. The transcript file
	// may not have been flushed yet when the Stop hook runs, so it can be
	// missing from the JSONL.
	LastAssistantMessage string `json:"last_assistant_message"`
	CWD                  string `json:"cwd"`
	// Official hook input fields retained for future event/model-specific behavior.
	HookEventName string `json:"hook_event_name"`
	Model         string `json:"model"`
}

var uuidPattern = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// Run executes the hook subcommand.
func Run(args []string) {
	fs := flag.NewFlagSet("hook", flag.ExitOnError)
	dir := fs.String("dir", "", "Output directory (required)")
	base := fs.String("base", "", "Base path to strip from project directory for project name (e.g., ~/repos)")
	tools := fs.Bool("tools", false, "Include tool use summaries")
	all := fs.Bool("all", false, "Disable filtering (include all messages)")
	images := fs.Bool("images", false, "Extract and embed images")
	providerName := fs.String("provider", "auto", "Transcript provider: auto, claude, or codex")
	strict := fs.Bool("strict", false, "Fail when supported messages cannot be extracted safely")
	project := fs.String("project", "", "Explicit project name or relative project path")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: ccrec hook [-base <base-path>] -dir <output-directory>\n\n")
		fmt.Fprintf(os.Stderr, "Run as a Claude Code or Codex hook. Reads hook JSON from stdin,\n")
		fmt.Fprintf(os.Stderr, "converts the transcript to Markdown, and saves to the output directory.\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if *dir == "" {
		fs.Usage()
		os.Exit(1)
	}
	provider, err := parser.ParseProvider(*providerName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ccrec hook: %v\n", err)
		os.Exit(1)
	}

	// Expand ~ in dir path.
	outDir := expandHome(*dir)

	// Read stdin JSON.
	var input HookInput
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		fmt.Fprintf(os.Stderr, "ccrec hook: failed to read stdin: %v\n", err)
		os.Exit(1)
	}

	// Skip if already in a stop hook chain (prevent infinite loops).
	if input.StopHookActive {
		return
	}

	if input.TranscriptPath == "" {
		return
	}

	// Skip subagent transcripts.
	if strings.Contains(input.TranscriptPath, "/subagents/") {
		return
	}

	// Derive project name: CLAUDE_PROJECT_DIR > cwd > transcript path.
	claudeProjectDir := os.Getenv("CLAUDE_PROJECT_DIR")
	projectDir := claudeProjectDir
	if projectDir == "" {
		projectDir = input.CWD
	}
	basePath := ""
	if *base != "" {
		basePath = expandHome(*base)
	}
	projectName := *project
	if projectName == "" {
		projectName = deriveProjectName(projectDir, basePath, input.TranscriptPath)
	}
	projectName, err = safeProjectPath(projectName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ccrec hook: invalid project name: %v\n", err)
		os.Exit(1)
	}
	if projectName == "" {
		fmt.Fprintf(os.Stderr, "ccrec hook: could not determine project name from CLAUDE_PROJECT_DIR=%q cwd=%q transcript=%q\n", claudeProjectDir, input.CWD, input.TranscriptPath)
		os.Exit(1)
	}

	// Parse transcript. A missing file is not an error: with
	// --no-session-persistence, Claude Code passes a transcript_path
	// that was never written.
	result, err := parser.ParseFileWithOptions(input.TranscriptPath, parser.ParseOptions{Provider: provider, Strict: *strict, Images: *images})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		fmt.Fprintf(os.Stderr, "ccrec hook: parse error: %v\n", err)
		os.Exit(1)
	}
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Line > 0 {
			fmt.Fprintf(os.Stderr, "ccrec hook: warning: line %d: %s\n", diagnostic.Line, diagnostic.Message)
		} else {
			fmt.Fprintf(os.Stderr, "ccrec hook: warning: %s\n", diagnostic.Message)
		}
	}
	records := appendLastAssistantMessage(result.Records, input.LastAssistantMessage, result.Provider)

	if len(records) == 0 {
		return
	}

	// Skip when no meaningful messages exist after filtering to avoid
	// creating empty Markdown files and directories. Note: FormatMarkdown
	// applies the same filter internally; the duplication is intentional
	// to prevent file-system side effects before they happen.
	if !*all && len(formatter.FilterRecordsWithImages(records, *tools, *images)) == 0 {
		return
	}

	// Build output path: {dir}/{project}/{date}_{session_id}.md
	// Use the first non-zero timestamp (skip file-history-snapshot etc.)
	sessionDate := "unknown"
	for _, rec := range records {
		if !rec.Timestamp.IsZero() {
			sessionDate = rec.Timestamp.Local().Format("2006-01-02")
			break
		}
	}
	sessionID := resolveSessionID(input.SessionID, result.SessionID, input.TranscriptPath, result.Provider)
	baseName := sessionDate + "_" + sessionID
	if result.Provider == parser.ProviderCodex {
		baseName += "_codex"
	}
	fileName := baseName + ".md"

	outProjectDir := filepath.Join(outDir, projectName)
	if err := os.MkdirAll(outProjectDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "ccrec hook: mkdir %s: %v\n", outProjectDir, err)
		os.Exit(1)
	}

	outPath := filepath.Join(outProjectDir, fileName)
	opts := formatter.Options{
		SourcePath:     input.TranscriptPath,
		IncludeToolUse: *tools,
		IncludeAll:     *all,
		IncludeImages:  *images,
		AttachmentsDir: filepath.Join(outProjectDir, "attachments_"+baseName),
	}
	if err := safefile.Write(outPath, 0o600, func(w io.Writer) error {
		return formatter.FormatMarkdown(w, records, opts)
	}); err != nil {
		fmt.Fprintf(os.Stderr, "ccrec hook: format error: %v\n", err)
		os.Exit(1)
	}
}

// appendLastAssistantMessage appends msg as a final assistant record unless the
// transcript already contains it in the current turn (the assistant records
// after the last user message with visible text).
func appendLastAssistantMessage(records []*parser.Record, msg string, provider parser.Provider) []*parser.Record {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return records
	}
	for i := len(records) - 1; i >= 0; i-- {
		rec := records[i]
		text := strings.TrimSpace(recordVisibleText(rec))
		if rec.Role == "user" && text != "" && !rec.IsMeta {
			break
		}
		if rec.Role == "assistant" && text == msg {
			return records
		}
	}
	seq := 0
	if n := len(records); n > 0 {
		seq = records[n-1].Sequence + 1
	}
	rec := &parser.Record{
		Type:      "assistant",
		Role:      "assistant",
		Text:      msg,
		Timestamp: time.Now(),
		Sequence:  seq,
		Provider:  provider,
	}
	if provider == parser.ProviderCodex {
		rec.Phase = parser.PhaseFinal
	}
	return append(records, rec)
}

func recordVisibleText(rec *parser.Record) string {
	if rec.Text != "" {
		return rec.Text
	}
	if rec.Message == nil {
		return ""
	}
	return parser.ExtractText(rec.Message.Content)
}

// deriveProjectName determines the project name from the project directory (CLAUDE_PROJECT_DIR or cwd),
// an optional base path, and the transcript path.
// When basePath is set and the project directory is under it, the relative path is used (e.g., "my-app1/backend").
// Otherwise it falls back to filepath.Base of the project directory, then to ExtractProjectName from the transcript path.
func deriveProjectName(projectDir, basePath, transcriptPath string) string {
	if projectDir != "" {
		if basePath != "" {
			rel, err := filepath.Rel(basePath, projectDir)
			if err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				return rel
			}
		}
		return filepath.Base(projectDir)
	}
	return ExtractProjectName(transcriptPath)
}

// ExtractProjectName derives a project name from a Claude Code transcript path.
//
// Path format: ~/.claude/projects/-Users-username-repos-projectname/session-id.jsonl
// The project directory name is hyphen-separated path segments; the last segment is the project name.
func ExtractProjectName(transcriptPath string) string {
	dir := filepath.Dir(transcriptPath)

	// Walk up to find the project directory (starts with "-").
	// In Run(), subagent paths are skipped before reaching here,
	// but this function handles them defensively since it is exported.
	for {
		base := filepath.Base(dir)
		if strings.HasPrefix(base, "-") {
			// Found the project directory (starts with "-").
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	base := filepath.Base(dir)

	// The project directory is encoded as hyphen-separated path components.
	// e.g., "-Users-junyatakaichi-repos-ccrec" → "ccrec"
	parts := strings.Split(base, "-")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}

	return ""
}

// extractSessionID extracts a collision-resistant session ID from the
// transcript file name. Codex rollout prefixes are removed, while ordinary
// basenames are preserved in full.
func extractSessionID(transcriptPath string) string {
	base := filepath.Base(transcriptPath)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if strings.HasPrefix(base, "rollout-") {
		matches := uuidPattern.FindAllString(base, -1)
		if len(matches) > 0 {
			return matches[len(matches)-1]
		}
	}
	return base
}

func resolveSessionID(hookID, metadataID, transcriptPath string, provider parser.Provider) string {
	for _, candidate := range []string{hookID, metadataID, extractSessionID(transcriptPath)} {
		if sanitized := sanitizeSessionToken(candidate); sanitized != "" {
			if provider == parser.ProviderCodex {
				return shortSessionHash(candidate)
			}
			// Preserve legacy Claude filenames for standard UUIDs while keeping
			// custom or malformed IDs safely sanitized in full.
			if provider == parser.ProviderClaude && uuidPattern.FindString(candidate) == candidate {
				return candidate[:8]
			}
			return sanitized
		}
	}
	canonical, err := filepath.Abs(transcriptPath)
	if err != nil {
		canonical = transcriptPath
	}
	digest := sha256.Sum256([]byte(string(provider) + "\x00" + canonical))
	return fmt.Sprintf("session-%x", digest[:8])
}

func shortSessionHash(sessionID string) string {
	digest := sha256.Sum256([]byte(sessionID))
	return fmt.Sprintf("%x", digest[:4])
}

func sanitizeSessionToken(value string) string {
	if value == "" {
		return ""
	}
	original := value
	var b strings.Builder
	lastSeparator := false
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
			lastSeparator = false
			continue
		}
		if !lastSeparator {
			b.WriteByte('-')
			lastSeparator = true
		}
	}
	value = strings.Trim(b.String(), ".-_")
	digest := sha256.Sum256([]byte(original))
	if value == "" {
		return fmt.Sprintf("session-%x", digest[:8])
	}
	if value == original && len(value) <= 128 {
		return value
	}
	if len(value) > 111 {
		value = value[:111]
	}
	return fmt.Sprintf("%s-%x", value, digest[:8])
}

func safeProjectPath(value string) (string, error) {
	clean := filepath.Clean(value)
	if clean == "." || clean == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe relative path %q", value)
	}
	return clean, nil
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		if path == "~" {
			return home
		}
		return filepath.Join(home, path[2:])
	}
	return path
}
