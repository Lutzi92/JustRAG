package fetcher

import (
	"strings"
	"testing"
)

func TestStealthScriptNotEmpty(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(stealthScript) == "" {
		t.Fatal("stealthScript must not be empty")
	}
}

func TestStealthScriptContainsWebdriverOverride(t *testing.T) {
	t.Parallel()
	if !strings.Contains(stealthScript, "webdriver") {
		t.Error("stealthScript should override navigator.webdriver")
	}
}

func TestStealthScriptContainsChromeRuntime(t *testing.T) {
	t.Parallel()
	if !strings.Contains(stealthScript, "window.chrome") {
		t.Error("stealthScript should define window.chrome")
	}
}

func TestStealthScriptContainsPluginsOverride(t *testing.T) {
	t.Parallel()
	if !strings.Contains(stealthScript, "plugins") {
		t.Error("stealthScript should override navigator.plugins")
	}
}
