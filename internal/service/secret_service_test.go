package service

import "testing"

// TestParseSecretPathHappyPath 锁住 "org.proj.env.folder.KEY" 5 段解析。
func TestParseSecretPathHappyPath(t *testing.T) {
	org, proj, env, folder, key, err := parseSecretPath("o1.p1.dev.globals.FOO")
	if err != nil {
		t.Fatalf("parseSecretPath returned error: %v", err)
	}
	if org != "o1" || proj != "p1" || env != "dev" || folder != "globals" || key != "FOO" {
		t.Fatalf("unexpected segments: %q %q %q %q %q", org, proj, env, folder, key)
	}
}

// TestParseSecretPathEmptySegment 任一段为空都报错,防止半截路径泄漏。
func TestParseSecretPathEmptySegment(t *testing.T) {
	cases := []string{
		"o1.p1.dev.globals.",
		"o1.p1.dev..FOO",
		"o1.p1..globals.FOO",
		"o1..dev.globals.FOO",
		".p1.dev.globals.FOO",
	}
	for _, p := range cases {
		if _, _, _, _, _, err := parseSecretPath(p); err == nil {
			t.Fatalf("parseSecretPath(%q) should error on empty segment", p)
		}
	}
}

// TestParseSecretPathWrongSegmentCount 段数不是 5 时直接 reject。
func TestParseSecretPathWrongSegmentCount(t *testing.T) {
	cases := []string{
		"o1.p1.dev.globals",           // 4 段
		"o1.p1.dev.globals.FOO.EXTRA", // 6 段
		"o1.p1.FOO",                   // 3 段
		"",                            // 0 段
		"FOO",                         // 1 段
	}
	for _, p := range cases {
		if _, _, _, _, _, err := parseSecretPath(p); err == nil {
			t.Fatalf("parseSecretPath(%q) should error on wrong segment count", p)
		}
	}
}

// TestParseSecretPathLeadingTrailingSpace 路径两侧空白应被 TrimSpace 吃掉。
func TestParseSecretPathLeadingTrailingSpace(t *testing.T) {
	org, proj, env, folder, key, err := parseSecretPath("  o1.p1.dev.globals.FOO  ")
	if err != nil {
		t.Fatalf("parseSecretPath returned error: %v", err)
	}
	if org != "o1" || proj != "p1" || env != "dev" || folder != "globals" || key != "FOO" {
		t.Fatalf("unexpected segments after trim: %q %q %q %q %q", org, proj, env, folder, key)
	}
}
