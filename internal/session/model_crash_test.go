package session

import "testing"

func TestDetectModelCrashUsesCapturedFatalSignatureOnly(t *testing.T) {
	t.Parallel()

	const fatal = "The model has crashed without additional information. (Exit code: null)"
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{name: "exact", output: fatal, want: true},
		{name: "surrounding whitespace", output: "\n  " + fatal + "  \n", want: true},
		{name: "ansi and UI decoration", output: "\x1b[31m│ Error: " + fatal + "\x1b[0m", want: true},
		{
			name: "real OpenCode footer remains current fatal",
			output: fatal + `
│ Build · Qwen3.6 35B-A3B (local, loaded)
│ LM Studio (local)
│ ~/gt-setup · 42% context
╰────────────────────────────────────────`,
			want: true,
		},
		{
			name: "Linux OpenCode footer remains current fatal",
			output: fatal + `
│ Build · Llama 3.3 70B Instruct (local, loaded)
│ llama.cpp (local)
│ /home/alice/src/gastown · 18% context
╰────────────────────────────────────────`,
			want: true,
		},
		{
			name: "alternate local model OpenCode footer remains current fatal",
			output: fatal + `
│ Build · DeepSeek R1 14B (local, loaded)
│ Ollama (local)
│ /workspace/gastown · 91% context
╰────────────────────────────────────────`,
			want: true,
		},
		{name: "newer successful progress supersedes fatal", output: fatal + "\nassistant: recovery complete; continuing work", want: false},
		{name: "newer tool progress supersedes footer", output: fatal + "\n│ Build · Qwen3.6 35B-A3B (local, loaded)\nTool: reading AGENTS.md", want: false},
		{name: "footer-like prose is meaningful output", output: fatal + "\nassistant: Build · recovery is complete", want: false},
		{name: "connection refused belongs to infrastructure watchdog", output: "connection refused: LM Studio unavailable", want: false},
		{name: "transport failure belongs to infrastructure watchdog", output: "transport error while sending request", want: false},
		{name: "generic model error is not sufficient", output: "the model crashed", want: false},
		{name: "different exit code is not sufficient", output: "The model has crashed without additional information. (Exit code: 1)", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fingerprint, got := DetectModelCrash(tt.output)
			if got != tt.want {
				t.Fatalf("DetectModelCrash() matched = %v, want %v", got, tt.want)
			}
			if got && fingerprint != ModelCrashFatalFingerprint {
				t.Fatalf("fingerprint = %q, want %q", fingerprint, ModelCrashFatalFingerprint)
			}
		})
	}
}
