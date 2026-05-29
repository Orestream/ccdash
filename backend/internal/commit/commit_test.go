package commit

import (
	"context"
	"errors"
	"testing"
)

func TestFakeGenerator(t *testing.T) {
	f := FakeGenerator{Msg: "feat: hello"}
	got, err := f.Generate(context.Background(), []byte("diff"))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got != "feat: hello" {
		t.Fatalf("unexpected message: %q", got)
	}

	// Empty Msg falls back to the canonical fallback.
	got, _ = FakeGenerator{}.Generate(context.Background(), nil)
	if got != FallbackMessage {
		t.Fatalf("expected fallback when Msg empty, got %q", got)
	}
}

func TestGenerateOrFallbackNilGenerator(t *testing.T) {
	got := GenerateOrFallback(context.Background(), nil, []byte("x"))
	if got != FallbackMessage {
		t.Fatalf("nil generator should fall back, got %q", got)
	}
}

type erroringGen struct{}

func (erroringGen) Generate(context.Context, []byte) (string, error) {
	return "ignored", errors.New("boom")
}

type emptyGen struct{}

func (emptyGen) Generate(context.Context, []byte) (string, error) { return "   \n", nil }

func TestGenerateOrFallbackErrorAndEmpty(t *testing.T) {
	if got := GenerateOrFallback(context.Background(), erroringGen{}, nil); got != FallbackMessage {
		t.Fatalf("erroring generator should fall back, got %q", got)
	}
	if got := GenerateOrFallback(context.Background(), emptyGen{}, nil); got != FallbackMessage {
		t.Fatalf("empty generator should fall back, got %q", got)
	}
}

func TestClaudeGeneratorMissingBinaryReturnsError(t *testing.T) {
	// Choose a path that almost certainly doesn't exist so exec fails. The
	// caller (GenerateOrFallback) will fall back; this test asserts the lower
	// layer surfaces an error rather than silently returning "".
	g := ClaudeGenerator{Bin: "/no/such/binary/ccdash-test-claude"}
	if _, err := g.Generate(context.Background(), []byte("diff")); err == nil {
		t.Fatalf("expected error for missing binary")
	}
}

func TestClaudeGeneratorMissingBinaryFallsBack(t *testing.T) {
	g := ClaudeGenerator{Bin: "/no/such/binary/ccdash-test-claude"}
	got := GenerateOrFallback(context.Background(), g, []byte("diff"))
	if got != FallbackMessage {
		t.Fatalf("expected fallback on missing binary, got %q", got)
	}
}

func TestFirstLine(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		"   ":                 "",
		"hello\nworld":        "hello",
		"\n\n  feat: x\nbody": "feat: x",
	}
	for in, want := range cases {
		if got := firstLine(in); got != want {
			t.Fatalf("firstLine(%q) = %q, want %q", in, got, want)
		}
	}
}
