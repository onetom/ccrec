package hook

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/capybara-translation/ccrec/internal/parser"
)

const hookTinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func TestExtractProjectName(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "standard project path",
			path: "/Users/junya/.claude/projects/-Users-junya-repos-ccrec/abc123.jsonl",
			want: "ccrec",
		},
		{
			name: "workspace path",
			path: "/Users/junya/.claude/projects/-Users-junya-workspace/abc123.jsonl",
			want: "workspace",
		},
		{
			name: "nested project path",
			path: "/Users/junya/.claude/projects/-Users-junya-repos-my-app/abc123.jsonl",
			want: "app",
		},
		{
			name: "subagent path",
			path: "/Users/junya/.claude/projects/-Users-junya-repos-ccrec/abc123/subagents/agent-xyz.jsonl",
			want: "ccrec",
		},
		{
			name: "deep nested path",
			path: "/Users/junya/.claude/projects/-Users-junya-repos-goblog/session/subagents/agent-a19db5a.jsonl",
			want: "goblog",
		},
		{
			name: "path with trailing hyphens",
			path: "/Users/junya/.claude/projects/-Users-junya-repos-go-todo-app-my/abc.jsonl",
			want: "my",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractProjectName(tt.path)
			if got != tt.want {
				t.Errorf("ExtractProjectName(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestDeriveProjectName(t *testing.T) {
	tests := []struct {
		name           string
		projectDir     string
		basePath       string
		transcriptPath string
		want           string
	}{
		{
			name:       "base strips prefix from project dir",
			projectDir: "/Users/junya/repos/my-app1/backend",
			basePath:   "/Users/junya/repos",
			want:       "my-app1/backend",
		},
		{
			name:       "base strips single level",
			projectDir: "/Users/junya/repos/my-app1",
			basePath:   "/Users/junya/repos",
			want:       "my-app1",
		},
		{
			name:       "project dir not under base falls back to Base",
			projectDir: "/other/path/project",
			basePath:   "/Users/junya/repos",
			want:       "project",
		},
		{
			name:       "project dir equals base falls back to Base",
			projectDir: "/Users/junya/repos",
			basePath:   "/Users/junya/repos",
			want:       "repos",
		},
		{
			name:       "no base uses filepath.Base",
			projectDir: "/Users/junya/repos/myproject",
			want:       "myproject",
		},
		{
			name:           "no project dir falls back to transcript",
			transcriptPath: "/Users/junya/.claude/projects/-Users-junya-repos-ccrec/abc123.jsonl",
			want:           "ccrec",
		},
		{
			name:       "trailing slashes on both paths",
			projectDir: "/Users/junya/repos/my-app1/backend/",
			basePath:   "/Users/junya/repos/",
			want:       "my-app1/backend",
		},
		{
			name:           "base set but project dir empty falls back to transcript",
			basePath:       "/Users/junya/repos",
			transcriptPath: "/Users/junya/.claude/projects/-Users-junya-repos-ccrec/abc123.jsonl",
			want:           "ccrec",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveProjectName(tt.projectDir, tt.basePath, tt.transcriptPath)
			if got != tt.want {
				t.Errorf("deriveProjectName(%q, %q, %q) = %q, want %q",
					tt.projectDir, tt.basePath, tt.transcriptPath, got, tt.want)
			}
		})
	}
}

func TestExtractSessionID(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "Codex rollout filename",
			path: "/path/to/rollout-2026-09-17T18-22-44-01a0aeac-efdc-7e71-869a-157d6c25761c.jsonl",
			want: "01a0aeac-efdc-7e71-869a-157d6c25761c",
		},
		{
			name: "standard UUID filename",
			path: "/path/to/42bb222a-a575-4386-bae8-2b0ce9a93d40.jsonl",
			want: "42bb222a-a575-4386-bae8-2b0ce9a93d40",
		},
		{
			name: "short filename",
			path: "/path/to/abc.jsonl",
			want: "abc",
		},
		{
			name: "no hyphens long name",
			path: "/path/to/abcdefghijklmnop.jsonl",
			want: "abcdefghijklmnop",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSessionID(tt.path)
			if got != tt.want {
				t.Errorf("extractSessionID(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestResolveSessionID_ClaudeUsesLegacyEightCharacterUUIDPrefix(t *testing.T) {
	got := resolveSessionID("42bb222a-a575-4386-bae8-2b0ce9a93d40", "", "/tmp/ignored.jsonl", parser.ProviderClaude)
	if got != "42bb222a" {
		t.Fatalf("resolved session ID = %q, want legacy Claude UUID prefix", got)
	}
}

func TestResolveSessionID_PrefersHookInputAndHashesItForCodex(t *testing.T) {
	got := resolveSessionID("../hook/session id", "metadata-id", "/tmp/rollout-2026-09-17T00-00-00-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl", parser.ProviderCodex)
	if got != "dc325de2" {
		t.Fatalf("resolved session ID = %q, want hash of the hook session ID", got)
	}
}

func TestSanitizeSessionToken_DifferentUnsafeIDsDoNotCollide(t *testing.T) {
	withSlash := sanitizeSessionToken("a/b")
	withSpace := sanitizeSessionToken("a b")
	if withSlash == withSpace {
		t.Fatalf("different unsafe IDs collided at %q", withSlash)
	}
}

func TestResolveSessionID_UsesCodexMetadataBeforeFilename(t *testing.T) {
	got := resolveSessionID("", "metadata-full-id", "/tmp/rollout-2026-09-17T00-00-00-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl", parser.ProviderCodex)
	if got != "75ea97cc" {
		t.Fatalf("resolved session ID = %q, want hash of metadata-full-id", got)
	}
}

func TestResolveSessionID_CodexUsesEightCharacterHashOfFullID(t *testing.T) {
	first := resolveSessionID("01a0aeac-efdc-7e71-869a-157d6c25761c", "", "/tmp/ignored.jsonl", parser.ProviderCodex)
	second := resolveSessionID("01a0aeac-1111-2222-3333-444444444444", "", "/tmp/ignored.jsonl", parser.ProviderCodex)

	if first != "5ddecb61" {
		t.Fatalf("first resolved session ID = %q, want 5ddecb61", first)
	}
	if second != "dca5187d" {
		t.Fatalf("second resolved session ID = %q, want dca5187d", second)
	}
}

func TestResolveSessionID_CodexHashesFilenameFallback(t *testing.T) {
	got := resolveSessionID("", "", "/tmp/rollout-2026-09-17T00-00-00-01a0aeac-efdc-7e71-869a-157d6c25761c.jsonl", parser.ProviderCodex)
	if got != "5ddecb61" {
		t.Fatalf("resolved session ID = %q, want hash of the complete filename ID", got)
	}
}

func TestResolveSessionID_ClaudeShortensFilenameFallback(t *testing.T) {
	got := resolveSessionID("", "", "/tmp/42bb222a-a575-4386-bae8-2b0ce9a93d40.jsonl", parser.ProviderClaude)
	if got != "42bb222a" {
		t.Fatalf("resolved session ID = %q, want legacy Claude filename prefix", got)
	}
}

func TestResolveSessionID_ClaudeKeepsSafeCustomID(t *testing.T) {
	got := resolveSessionID("custom-session-id", "", "/tmp/ignored.jsonl", parser.ProviderClaude)
	if got != "custom-session-id" {
		t.Fatalf("resolved session ID = %q, want full custom Claude ID", got)
	}
}

func TestRunIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Build the binary.
	binPath := filepath.Join(t.TempDir(), "ccrec")
	build := exec.Command("go", "build", "-o", binPath, "../../cmd/ccrec")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	// Create a minimal transcript file.
	transcriptDir := t.TempDir()
	transcriptPath := filepath.Join(transcriptDir, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl")
	transcriptContent := `{"type":"user","message":{"role":"user","content":"hello"},"timestamp":"2026-01-15T10:00:00Z"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]},"timestamp":"2026-01-15T10:00:01Z"}
`
	if err := os.WriteFile(transcriptPath, []byte(transcriptContent), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name           string
		claudeDir      string // CLAUDE_PROJECT_DIR env var (empty = unset)
		cwd            string // cwd field in stdin JSON
		base           string // -base flag (empty = omit)
		wantProjectDir string // expected subdirectory under output dir
	}{
		{
			name:           "CLAUDE_PROJECT_DIR takes priority over cwd",
			claudeDir:      "/Users/junya/repos/my-app",
			cwd:            "/Users/junya/repos/other-app",
			wantProjectDir: "my-app",
		},
		{
			name:           "CLAUDE_PROJECT_DIR with base",
			claudeDir:      "/Users/junya/repos/my-app/backend",
			cwd:            "/Users/junya/repos/other-app",
			base:           "/Users/junya/repos",
			wantProjectDir: "my-app/backend",
		},
		{
			name:           "falls back to cwd when CLAUDE_PROJECT_DIR is unset",
			claudeDir:      "",
			cwd:            "/Users/junya/repos/fallback-app",
			wantProjectDir: "fallback-app",
		},
		{
			name:           "cwd with base when CLAUDE_PROJECT_DIR is unset",
			claudeDir:      "",
			cwd:            "/Users/junya/repos/my-app/frontend",
			base:           "/Users/junya/repos",
			wantProjectDir: "my-app/frontend",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outDir := t.TempDir()

			args := []string{"hook", "-dir", outDir}
			if tt.base != "" {
				args = append(args, "-base", tt.base)
			}

			input := HookInput{
				TranscriptPath: transcriptPath,
				CWD:            tt.cwd,
			}
			stdinBytes, _ := json.Marshal(input)

			cmd := exec.Command(binPath, args...)
			cmd.Stdin = strings.NewReader(string(stdinBytes))
			cmd.Env = append(os.Environ(), "CLAUDE_PROJECT_DIR="+tt.claudeDir)
			if tt.claudeDir == "" {
				// Ensure it's truly unset.
				env := []string{}
				for _, e := range os.Environ() {
					if !strings.HasPrefix(e, "CLAUDE_PROJECT_DIR=") {
						env = append(env, e)
					}
				}
				cmd.Env = env
			}

			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("hook failed: %v\n%s", err, out)
			}

			// Verify the output file was created in the expected project directory.
			expectedDir := filepath.Join(outDir, tt.wantProjectDir)
			expectedPath := filepath.Join(expectedDir, "2026-01-15_aaaaaaaa.md")
			if _, err := os.Stat(expectedPath); err != nil {
				t.Errorf("expected Claude hook output %s: %v", expectedPath, err)
			}
		})
	}
}

func TestRunIntegration_SkipsEmptyOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Build the binary.
	binPath := filepath.Join(t.TempDir(), "ccrec")
	build := exec.Command("go", "build", "-o", binPath, "../../cmd/ccrec")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	// Transcript with only meta/system records — no meaningful user or assistant messages.
	metaOnlyContent := `{"type":"user","isMeta":true,"message":{"role":"user","content":"meta"},"timestamp":"2026-01-15T10:00:00Z"}
{"type":"system","message":{"role":"system","content":"system info"},"timestamp":"2026-01-15T10:00:01Z"}
`
	// Transcript with only tool_use (no text) — filtered out without --tools.
	toolOnlyContent := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/tmp/test.go"}}]},"timestamp":"2026-01-15T10:00:00Z"}
`
	invalidCodexImageOnlyContent := `{"type":"session_meta","payload":{"id":"session-id"}}
{"timestamp":"2026-01-15T10:00:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,bm90IGFuIGltYWdl"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["user.image"],"turn_id":"turn-1"}}}
`

	tests := []struct {
		name       string
		transcript string
		extraArgs  []string
		wantFile   bool
	}{
		{
			name:       "meta-only transcript produces no output",
			transcript: metaOnlyContent,
			wantFile:   false,
		},
		{
			name:       "tool-only transcript without --tools produces no output",
			transcript: toolOnlyContent,
			wantFile:   false,
		},
		{
			name:       "tool-only transcript with --tools produces output",
			transcript: toolOnlyContent,
			extraArgs:  []string{"-tools"},
			wantFile:   true,
		},
		{
			name:       "meta-only transcript with --all produces output",
			transcript: metaOnlyContent,
			extraArgs:  []string{"-all"},
			wantFile:   true,
		},
		{
			name:       "invalid Codex image-only transcript produces no output",
			transcript: invalidCodexImageOnlyContent,
			extraArgs:  []string{"-provider", "codex", "-images"},
			wantFile:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outDir := t.TempDir()
			transcriptDir := t.TempDir()
			transcriptPath := filepath.Join(transcriptDir, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl")
			if err := os.WriteFile(transcriptPath, []byte(tt.transcript), 0o644); err != nil {
				t.Fatal(err)
			}

			args := []string{"hook", "-dir", outDir}
			args = append(args, tt.extraArgs...)

			input := HookInput{
				TranscriptPath: transcriptPath,
				CWD:            "/Users/junya/repos/test-project",
			}
			stdinBytes, _ := json.Marshal(input)

			cmd := exec.Command(binPath, args...)
			cmd.Stdin = strings.NewReader(string(stdinBytes))
			// Remove CLAUDE_PROJECT_DIR to use cwd fallback.
			env := []string{}
			for _, e := range os.Environ() {
				if !strings.HasPrefix(e, "CLAUDE_PROJECT_DIR=") {
					env = append(env, e)
				}
			}
			cmd.Env = env

			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("hook failed: %v\n%s", err, out)
			}

			projectDir := filepath.Join(outDir, "test-project")
			entries, _ := os.ReadDir(projectDir)
			found := false
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".md") {
					found = true
					break
				}
			}

			if tt.wantFile && !found {
				t.Errorf("expected .md file in %s but none found", projectDir)
			}
			if !tt.wantFile && found {
				t.Errorf("expected no .md file in %s but one was created", projectDir)
			}
		})
	}
}

func TestRunIntegration_SkipsUnavailableTranscript(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Build the binary.
	binPath := filepath.Join(t.TempDir(), "ccrec")
	build := exec.Command("go", "build", "-o", binPath, "../../cmd/ccrec")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	missingPath := filepath.Join(t.TempDir(), "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl")
	tests := []struct {
		name  string
		stdin string
	}{
		{name: "null transcript path", stdin: `{"transcript_path":null,"cwd":"/Users/junya/repos/test-project"}`},
		{name: "empty transcript path", stdin: `{"transcript_path":"","cwd":"/Users/junya/repos/test-project"}`},
		{name: "missing transcript path", stdin: `{"transcript_path":"` + missingPath + `","cwd":"/Users/junya/repos/test-project"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outDir := t.TempDir()
			cmd := exec.Command(binPath, "hook", "-dir", outDir)
			cmd.Stdin = strings.NewReader(tt.stdin)
			env := []string{}
			for _, e := range os.Environ() {
				if !strings.HasPrefix(e, "CLAUDE_PROJECT_DIR=") {
					env = append(env, e)
				}
			}
			cmd.Env = env

			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("hook should exit 0, got: %v\n%s", err, out)
			}
			if len(out) != 0 {
				t.Errorf("hook should produce no output, got: %s", out)
			}
			entries, _ := os.ReadDir(outDir)
			if len(entries) != 0 {
				t.Errorf("hook should create no files, found %d entries in %s", len(entries), outDir)
			}
		})
	}
}

func TestRunIntegration_CodexSessionEndIsIdempotentAndUsesHashedHookSessionID(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	binPath := filepath.Join(t.TempDir(), "ccrec")
	build := exec.Command("go", "build", "-o", binPath, "../../cmd/ccrec")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	fixture, err := os.ReadFile(filepath.Join("..", "parser", "testdata", "codex-0.145.0.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	fixture = append(fixture, []byte(`{"timestamp":"2026-09-17T12:00:03Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"image prompt"},{"type":"input_image","image_url":"data:image/png;base64,`+hookTinyPNGBase64+`"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["user.text","user.image"],"turn_id":"image-turn"}}}
{"timestamp":"2026-09-17T12:00:04Z","type":"event_msg","payload":{"type":"item_completed","turn_id":"image-turn","item":{"type":"UserMessage","content":[{"type":"text","text":"image prompt"},{"type":"local_image","path":"/must/not/be/read.png"}]}}}
`)...)
	transcriptPath := filepath.Join(t.TempDir(), "rollout-2026-09-17T00-00-00-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jsonl")
	if err := os.WriteFile(transcriptPath, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	input := HookInput{
		SessionID:      "hook-session-id",
		TranscriptPath: transcriptPath,
		CWD:            "/workspace/example",
		HookEventName:  "SessionEnd",
		Model:          "gpt-test",
	}
	stdinBytes, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}

	run := func() []byte {
		t.Helper()
		cmd := exec.Command(binPath, "hook", "-provider", "codex", "-project", "codex-project", "-images", "-dir", outDir)
		cmd.Stdin = strings.NewReader(string(stdinBytes))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Codex hook failed: %v\n%s", err, out)
		}
		path := filepath.Join(outDir, "codex-project", "2026-09-17_32c723f6_codex.md")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read output: %v", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("output mode = %o, want 600", got)
		}
		return data
	}

	first := run()
	second := run()
	if string(first) != string(second) {
		t.Fatal("running the same Codex SessionEnd hook twice changed the output")
	}
	if !strings.Contains(string(second), "fixture prompt") || !strings.Contains(string(second), "fixture answer") {
		t.Fatalf("Codex messages missing from output:\n%s", second)
	}
	attachment := filepath.Join(outDir, "codex-project", "attachments_2026-09-17_32c723f6_codex", "image_001.png")
	if _, err := os.Stat(attachment); err != nil {
		t.Fatalf("Codex image missing: %v", err)
	}

	if runtime.GOOS != "windows" {
		blockedOutDir := t.TempDir()
		projectDir := filepath.Join(blockedOutDir, "codex-project")
		if err := os.Mkdir(projectDir, 0o700); err != nil {
			t.Fatal(err)
		}
		realDir := filepath.Join(blockedOutDir, "real")
		if err := os.Mkdir(realDir, 0o700); err != nil {
			t.Fatal(err)
		}
		attachmentsDir := filepath.Join(projectDir, "attachments_2026-09-17_32c723f6_codex")
		if err := os.Symlink(realDir, attachmentsDir); err != nil {
			t.Fatal(err)
		}

		cmd := exec.Command(binPath, "hook", "-provider", "codex", "-project", "codex-project", "-images", "-dir", blockedOutDir)
		cmd.Stdin = strings.NewReader(string(stdinBytes))
		if output, err := cmd.CombinedOutput(); err == nil {
			t.Fatalf("hook succeeded with a symlinked attachments directory:\n%s", output)
		}
		markdownPath := filepath.Join(projectDir, "2026-09-17_32c723f6_codex.md")
		if _, err := os.Stat(markdownPath); !os.IsNotExist(err) {
			t.Fatalf("hook published Markdown despite image save failure: %v", err)
		}
	}
}

func TestExpandHome(t *testing.T) {
	got := expandHome("/absolute/path")
	if got != "/absolute/path" {
		t.Errorf("absolute path should be unchanged, got %q", got)
	}

	got = expandHome("~/Documents")
	if got == "~/Documents" {
		t.Error("~/Documents should be expanded")
	}

	got = expandHome("~")
	if got == "~" {
		t.Error("bare ~ should be expanded")
	}
}

func TestAppendLastAssistantMessage(t *testing.T) {
	user := &parser.Record{Role: "user", Text: "hi"}
	asst := &parser.Record{Role: "assistant", Text: "done"}
	toolResult := &parser.Record{Role: "user"}

	got := appendLastAssistantMessage([]*parser.Record{user}, " done\n", parser.ProviderClaude)
	if len(got) != 2 || got[1].Text != "done" || got[1].Role != "assistant" {
		t.Fatalf("missing message not appended: %+v", got)
	}
	got = appendLastAssistantMessage([]*parser.Record{user, asst, toolResult}, "done", parser.ProviderClaude)
	if len(got) != 3 {
		t.Fatalf("duplicate appended: %d records", len(got))
	}
	// Same text in an earlier turn does not count as present.
	got = appendLastAssistantMessage([]*parser.Record{asst, user}, "done", parser.ProviderClaude)
	if len(got) != 3 {
		t.Fatalf("earlier-turn text wrongly deduped: %d records", len(got))
	}
	if got := appendLastAssistantMessage([]*parser.Record{user}, "  ", parser.ProviderClaude); len(got) != 1 {
		t.Fatalf("empty message appended")
	}
}
