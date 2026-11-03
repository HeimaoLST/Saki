package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"saki/desktop/locales"
)

func TestCatalogLanguageMatchingAndFallback(t *testing.T) {
	supported := []string{"en", "zh-Hans"}
	for _, preferred := range []string{"zh-Hans", "zh-CN", "zh-SG"} {
		if got := i18n.Match([]string{preferred}, supported); got != "zh-Hans" {
			t.Errorf("Match(%s) = %q, want zh-Hans", preferred, got)
		}
	}
	for _, preferred := range []string{"en", "en-US", "en-GB"} {
		if got := i18n.Match([]string{preferred}, supported); got != "en" {
			t.Errorf("Match(%s) = %q, want en", preferred, got)
		}
	}
	for _, preferred := range []string{"fr", "ja", "zh-Hant", "zh-TW", "zh-HK"} {
		if got := i18n.Match([]string{preferred}, supported); got != "" {
			t.Errorf("Match(%s) = %q, want no supported match", preferred, got)
		}
	}

	for _, tag := range []string{"zh-Hans", "zh-CN", "zh-SG"} {
		if got := locales.In(tag).SettingsTitle(); got != "设置" {
			t.Errorf("locales.In(%s).SettingsTitle() = %q", tag, got)
		}
	}
	for _, tag := range []string{"en", "en-US", "fr", "zh-Hant", "zh-TW", "zh-HK"} {
		if got := locales.In(tag).SettingsTitle(); got != "Settings" {
			t.Errorf("locales.In(%s).SettingsTitle() = %q, want English fallback", tag, got)
		}
	}
	if got := locales.Catalog.Languages(); !slices.Equal(got, []string{"en", "zh-Hans"}) {
		t.Errorf("catalog languages = %v", got)
	}
}

func TestUIStringsFollowTesterLocaleAndKeepDraftAndFocus(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Draft = "keep the conversation draft"
	a.openSettingsTab(settingsTabContact, settingsSourceRail)
	editor := a.settingsContactEditor()
	editor.Draft.Name = "Locale draft"
	a.settingsContactID = ct.ID

	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("Settings") || !tr.HasText("Contact profile") {
		t.Fatalf("English settings labels missing: %v", tr.Texts())
	}
	if err := tr.Click("Contact name"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused("Contact name") {
		t.Fatalf("contact field did not take focus: %v", tr.Texts())
	}
	if _, active := tr.TextCaret(); !active {
		t.Fatal("contact field should hold the text caret")
	}

	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText("设置") || !tr.HasText("联系人资料") || !tr.HasText("联系人资料：Saki") {
		t.Fatalf("Chinese settings labels missing: %v", tr.Texts())
	}
	if a.settingsContactEditor().Draft.Name != "Locale draft" {
		t.Fatalf("locale change rebuilt the profile draft: %q", a.settingsContactEditor().Draft.Name)
	}
	if ct.Draft != "keep the conversation draft" {
		t.Fatalf("locale change rebuilt the conversation draft: %q", ct.Draft)
	}
	if !tr.Focused("Contact name") {
		t.Fatalf("locale change lost field focus: %v", tr.Texts())
	}
	if _, active := tr.TextCaret(); !active {
		t.Fatal("locale change lost the text caret")
	}

	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("Settings") || !tr.HasText("Contact profile") {
		t.Fatalf("English labels did not return: %v", tr.Texts())
	}
	if !tr.Focused("Contact name") || a.settingsContactEditor().Draft.Name != "Locale draft" {
		t.Fatal("switching back rebuilt the field or draft")
	}
}

func TestSelectedSettingsAXFollowsTesterLocale(t *testing.T) {
	a, _ := workspaceTestApp(t)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("App actions") {
		t.Fatalf("English Settings rail AX missing: %v", tr.Texts())
	}
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText("应用操作") || tr.HasText("App actions") {
		t.Fatalf("Chinese Settings rail AX missing: %v", tr.Texts())
	}
	tr.SetLocale("en")
	tr.Frame()
	a.openSettingsTab(settingsTabAppearance, settingsSourceRail)
	a.settingsFocusBack = true
	tr.Frame()
	if !tr.Focused("← Back to workbench") || !tr.HasText("Settings navigation") {
		t.Fatalf("English Settings page AX missing: %v", tr.Texts())
	}
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.Focused("← 返回工作台") || !tr.HasText("设置导航") {
		t.Fatalf("Chinese Settings page AX missing: %v", tr.Texts())
	}
	if tr.HasText("Settings navigation") || tr.HasText("← Back to workbench") {
		t.Fatalf("English Settings page AX remained: %v", tr.Texts())
	}
}

func TestMarkdownCopyFeedbackFollowsTesterLocale(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		en   string
		zh   string
	}{
		{name: "success", en: "Copied", zh: "已复制"},
		{name: "failure", err: errors.New("clipboard unavailable"), en: "Copy failed: clipboard unavailable", zh: "复制失败：clipboard unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := ui.NewTester(func(c *ui.Context) {
				markdownReader(c, "source", markdownReaderOptions{
					CopyLabel: locales.In(c.Locale()).CopyMessageLabel(),
					CopyText:  func(string) error { return tc.err },
				})
			}, 500, 400)
			tr.SetLocale("en")
			tr.Frame()
			if err := tr.Click("Copy message"); err != nil {
				t.Fatal(err)
			}
			tr.Frame()
			revealCopyTooltip(t, tr, "Copy message")
			if !tr.HasText(tc.en) {
				t.Fatalf("English copy feedback missing: %v", tr.Texts())
			}
			tr.SetLocale("zh-Hans")
			tr.Frame()
			revealCopyTooltip(t, tr, "复制消息")
			if !tr.HasText(tc.zh) {
				t.Fatalf("Chinese copy feedback missing: %v", tr.Texts())
			}
		})
	}
}

func revealCopyTooltip(t *testing.T, tr *ui.Tester, label string) {
	t.Helper()
	r, ok := tr.Find(label)
	if !ok {
		t.Fatalf("copy control %q not found: %v", label, tr.Texts())
	}
	tr.Move(1, 1)
	tr.Move(r.X+r.W/2, r.Y+r.H/2)
	tr.Advance(700 * time.Millisecond)
	tr.Frame()
}

func TestCopyLabelsFollowTesterLocale(t *testing.T) {
	a := testApp(t)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("Copy message") {
		t.Fatalf("English copy label missing: %v", tr.Texts())
	}
	tr.SetLocale("zh-Hans")
	tr.Frame()
	if !tr.HasText("复制消息") {
		t.Fatalf("Chinese copy label missing: %v", tr.Texts())
	}
	tr.SetLocale("en")
	tr.Frame()
	if !tr.HasText("Copy message") {
		t.Fatalf("English copy label did not return: %v", tr.Texts())
	}
}

func TestPublishedCatalogContract(t *testing.T) {
	en := readCatalogFile(t, "en.json")
	zh := readCatalogFile(t, "zh-Hans.json")
	if err := checkCatalogContract(en, zh); err != nil {
		t.Fatal(err)
	}
	if got := readCatalogLocale(t, "zh-Hans.json"); got != "zh-Hans" {
		t.Fatalf("zh-Hans @@locale = %q", got)
	}
	entries, err := os.ReadDir("locales")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") && strings.Contains(strings.ToLower(entry.Name()), "xa") {
			t.Fatalf("pseudo locale must not be published: %s", entry.Name())
		}
	}
}

func TestCatalogContractAllowsArgumentReorder(t *testing.T) {
	en := map[string]string{
		"summary": "{name} has {count, plural, one {# file} other {# files}}",
	}
	zh := map[string]string{
		"summary": "{count, plural, other {# 个文件}}：{name}",
	}
	if err := checkCatalogContract(en, zh); err != nil {
		t.Fatalf("a legal argument reorder was rejected: %v", err)
	}
}

func TestCatalogContractRejectsMissingKeyAndWrongArgument(t *testing.T) {
	en := map[string]string{"greeting": "Hello {name}", "copyFailed": "Copy failed: {error}"}
	if err := checkCatalogContract(en, map[string]string{"greeting": "你好 {name}"}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing key contract = %v", err)
	}
	if err := checkCatalogContract(en, map[string]string{"greeting": "你好 {other}", "copyFailed": "复制失败：{error}"}); err == nil || !strings.Contains(err.Error(), "arguments") {
		t.Fatalf("argument contract = %v", err)
	}
}

func readCatalogLocale(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("locales", name))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	var tag string
	if err := json.Unmarshal(raw["@@locale"], &tag); err != nil {
		t.Fatal(err)
	}
	return tag
}

func readCatalogFile(t *testing.T, name string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("locales", name))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for key, value := range raw {
		if strings.HasPrefix(key, "@") {
			continue
		}
		var message string
		if err := json.Unmarshal(value, &message); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		out[key] = message
	}
	return out
}

func checkCatalogContract(en, translated map[string]string) error {
	var missing, extra []string
	for key := range en {
		if _, ok := translated[key]; !ok {
			missing = append(missing, key)
		}
	}
	for key := range translated {
		if _, ok := en[key]; !ok {
			extra = append(extra, key)
		}
	}
	slices.Sort(missing)
	slices.Sort(extra)
	if len(missing) > 0 {
		return fmt.Errorf("translation missing keys: %s", strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		return fmt.Errorf("translation has extra keys: %s", strings.Join(extra, ", "))
	}
	for _, key := range sortedMessageKeys(en) {
		source, err := i18n.ParseMessage(en[key])
		if err != nil {
			return fmt.Errorf("%s source: %w", key, err)
		}
		target, err := i18n.ParseMessage(translated[key])
		if err != nil {
			return fmt.Errorf("%s translation: %w", key, err)
		}
		if !sameArgs(source, target) {
			return fmt.Errorf("%s translation arguments differ: source %v, translation %v", key, source.Args(), target.Args())
		}
	}
	return nil
}

func sameArgs(source, target *i18n.Message) bool {
	sourceArgs := map[string]bool{}
	for _, arg := range source.Args() {
		sourceArgs[arg.Name+"\x00"+arg.Kind] = true
	}
	targetArgs := map[string]bool{}
	for _, arg := range target.Args() {
		targetArgs[arg.Name+"\x00"+arg.Kind] = true
	}
	if len(sourceArgs) != len(targetArgs) {
		return false
	}
	for arg := range sourceArgs {
		if !targetArgs[arg] {
			return false
		}
	}
	return true
}

func sortedMessageKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
