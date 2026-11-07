package main

import (
	"strings"
	"testing"
)

func TestPanelVersionCardIsBelowLogsAndLinksReleases(t *testing.T) {
	html := string(panelHTML)
	logsAt := strings.Index(html, `id="logs"`)
	cardAt := strings.Index(html, `id="versionCard"`)
	if logsAt < 0 || cardAt < 0 || cardAt < logsAt {
		t.Fatalf("版本卡片必须位于实时日志之后: logs=%d card=%d", logsAt, cardAt)
	}
	for _, want := range []string{
		`id="currentVersion"`,
		`id="latestVersion"`,
		`id="versionConnection"`,
		`https://gh-proxy.com/https://github.com/glance522/rf4_server_simulator/releases`,
		`https://github.com/glance522/rf4_server_simulator/releases`,
		`update_status`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("面板缺少版本卡片要素 %q", want)
		}
	}
}
