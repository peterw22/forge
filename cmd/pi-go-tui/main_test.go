package main

import (
	"reflect"
	"testing"
)

func TestNormalizeResumeArgs(t *testing.T) {
	tests := []struct{ input, want []string }{
		{[]string{"-r"}, []string{"-r=" + resumeSelector}},
		{[]string{"-r", "latest"}, []string{"-r", "latest"}},
		{[]string{"-r", "019abc"}, []string{"-r", "019abc"}},
		{[]string{"-r", "--model", "gpt"}, []string{"-r=" + resumeSelector, "--model", "gpt"}},
	}
	for _, test := range tests {
		if got := normalizeResumeArgs(test.input); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("normalizeResumeArgs(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestBashToolSummaryUsesDescription(t *testing.T) {
	got := toolSummary("bash", map[string]any{"command": "rm -rf build", "description": "Remove generated build artifacts"})
	if got != "bash · Remove generated build artifacts" {
		t.Fatalf("summary = %q", got)
	}
}

func TestCompletions(t *testing.T) {
	if got, want := completions("/cle"), []string{"/clear"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("command completion = %#v, want %#v", got, want)
	}
	if got, want := completions("/model gpt-5.6-t"), []string{"/model gpt-5.6-terra"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("model completion = %#v, want %#v", got, want)
	}
	if got, want := completions("/model gpt-6"), []string{"/model gpt-6-astra", "/model gpt-6-sol", "/model gpt-6-luna"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("GPT-6 model completion = %#v, want %#v", got, want)
	}
	if got := completions("ordinary prompt"); got != nil {
		t.Fatalf("prompt completion = %#v, want nil", got)
	}
}
