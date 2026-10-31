package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestNavigationSurfacesStayMutuallyExclusiveAndKeepState(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	a.workspaceOpen = false
	a.detail = ""
	ct.Issues = []*productIssue{{ID: "i-nav", ContactID: ct.ID, Goal: "navigable issue", Status: issueStatusInProgress}}
	for i := 0; i < 30; i++ {
		ct.History = append(ct.History, historyEntry{ID: fmt.Sprintf("h-nav-%02d", i), Date: "Today", From: "contact", Text: fmt.Sprintf("history marker %02d", i)})
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()

	if err := tr.Click("Work"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.detailIs(detailWork) || !tr.HasText("navigable issue") || !tr.Focused("Close work") {
		t.Fatalf("Work entry = detail=%q focusedClose=%v", a.activeDetail(), tr.Focused("Close work"))
	}

	if err := tr.Click("History"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.detailIs(detailHistory) || !tr.HasText("History · Saki") {
		t.Fatalf("History replaced Work = detail=%q", a.activeDetail())
	}
	a.historyQuery = "history marker"
	tr.Frame()
	a.historyScroll.Y = 37
	if err := tr.Click("Work"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.detailIs(detailWork) {
		t.Fatalf("Work did not replace History: %q", a.activeDetail())
	}
	if err := tr.Click("History"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.historyQuery != "history marker" || a.historyScroll.Y != 37 {
		t.Fatalf("History state lost across surfaces: query=%q scroll=%v", a.historyQuery, a.historyScroll.Y)
	}

	if err := tr.Click("Contact profile"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.detailIs(detailProfile) || a.settingsOpen {
		t.Fatalf("profile entry = detail=%q settings=%v", a.activeDetail(), a.settingsOpen)
	}
	ct.Purpose = "effective current purpose"
	ct.Instructions = "effective current instructions"
	tr.Frame()
	if !tr.HasText("effective current purpose") || !tr.HasText("effective current instructions") {
		t.Fatalf("profile summary did not read current contact facts: %v", tr.Texts())
	}
	if err := tr.Click("Edit full profile"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.settingsOpen || a.settingsTab != settingsTabContact {
		t.Fatalf("Edit profile = settings=%v tab=%q", a.settingsOpen, a.settingsTab)
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.detailIs(detailProfile) || !tr.Focused("Edit full profile") {
		t.Fatalf("Settings return = detail=%q focusedEdit=%v", a.activeDetail(), tr.Focused("Edit full profile"))
	}
}

func TestExplicitWorkEntryAlwaysSelectsWorkCarrier(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.windowWidth = 1040
	a.openDetail(detailHistory)
	a.setWorkspaceOpen(true)
	if !a.detailIs(detailWork) {
		t.Fatalf("explicit Work open kept %q", a.activeDetail())
	}
	a.openDetail(detailProfile)
	a.setWorkspaceOpen(true)
	if !a.detailIs(detailWork) {
		t.Fatalf("explicit Work open from profile kept %q", a.activeDetail())
	}
}

func TestNarrowDetailPageFocusesVisibleBackAcrossWidthChange(t *testing.T) {
	for _, kind := range []detailKind{detailWork, detailHistory, detailProfile} {
		t.Run(string(kind), func(t *testing.T) {
			a, _ := workspaceTestApp(t)
			a.windowWidth = 1040
			a.workspaceOpen = false
			a.detail = ""
			a.openDetail(kind)
			tr := ui.NewTester(a.view, 1040, 720)
			tr.Frame()
			closeLabel := "Close work"
			switch kind {
			case detailHistory:
				closeLabel = "Close history"
			case detailProfile:
				closeLabel = "Close profile"
			}
			if !a.detailIs(kind) || !tr.Focused(closeLabel) {
				t.Fatalf("wide %s did not focus its close control", kind)
			}

			a.windowWidth = 720
			tr.SetSize(720, 640)
			tr.Frame()
			back, ok := tr.Find(closeLabel)
			if !ok || back.W <= 0 || back.H <= 0 || back.X < 0 || back.Y < 0 || back.X+back.W > 720 || back.Y+back.H > 640 {
				t.Fatalf("narrow %s Back rect = %+v/%v", kind, back, ok)
			}
			if !a.detailPageActive() || !tr.Focused(closeLabel) {
				t.Fatalf("narrow %s did not focus the visible Back", kind)
			}
			if tr.HasText("Composer for Saki") || tr.HasText("Add contact") {
				t.Fatalf("narrow %s left the workbench background mounted: %v", kind, tr.Texts())
			}
			for i := 0; i < 3; i++ {
				tr.Key(0, ui.KeyTab)
				tr.Frame()
				if tr.Focused("Composer for Saki") || tr.Focused("Add contact") {
					t.Fatalf("narrow %s Tab reached an unmounted background control", kind)
				}
			}

			a.windowWidth = 1040
			tr.SetSize(1040, 720)
			tr.Frame()
			if a.detailPageActive() || !tr.Focused(closeLabel) {
				t.Fatalf("wide return for %s did not focus the visible Close", kind)
			}
		})
	}
}

func TestResizeDoesNotStealSettingsFocus(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.windowWidth = 1040
	a.workspaceOpen = false
	a.detail = ""
	a.openDetail(detailHistory)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	tr.Key(ui.Cmd, ui.KeyComma)
	tr.Frame()
	if !a.settingsOpen || !tr.Focused("← Back to workbench") {
		t.Fatal("Settings did not open with its own Back focused")
	}
	a.windowWidth = 720
	tr.SetSize(720, 640)
	tr.Frame()
	if !a.settingsOpen || !tr.Focused("← Back to workbench") {
		t.Fatalf("resize stole Settings focus: settings=%v texts=%v", a.settingsOpen, tr.Texts())
	}
	if tr.HasText("Composer for Saki") || tr.HasText("Add contact") {
		t.Fatal("Settings resize mounted the workbench background")
	}
}

func TestHistoryFullLateResultCannotHijackAnotherSurface(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	a.openDetail(detailHistory)
	a.contactHistory = &contactHistoryResult{Matches: []contactHistoryMatch{{SessionID: "s1", EntryID: "h1"}}}
	searchGen := a.contactHistoryGen
	detailGen := a.contactHistoryDetailGen
	a.contactHistoryDetailBusy = true
	a.openDetail(detailWork)
	a.applyContactHistoryFull(nil, a.connectionGeneration, ct.ID, searchGen, detailGen,
		&contactHistoryFull{SessionID: "s1", EntryID: "h1", Text: "late full"}, "", nil)
	if a.contactHistoryFull != nil || !a.detailIs(detailWork) {
		t.Fatalf("late full opened over Work: full=%+v detail=%q", a.contactHistoryFull, a.activeDetail())
	}
	if a.contactHistory == nil || len(a.contactHistory.Matches) != 1 {
		t.Fatal("late full cancellation lost the search result")
	}
	if a.contactHistoryDetailBusy {
		t.Fatal("late full left the display busy")
	}
}

func TestHistoryFullLateResultCannotOpenAfterHistoryClosed(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	a.openDetail(detailHistory)
	searchGen := a.contactHistoryGen
	detailGen := a.contactHistoryDetailGen
	a.closeDetail()
	a.applyContactHistoryFull(nil, a.connectionGeneration, ct.ID, searchGen, detailGen,
		&contactHistoryFull{SessionID: "s1", EntryID: "h1", Text: "late full"}, "", nil)
	if a.contactHistoryFull != nil || a.workspaceOpen {
		t.Fatalf("late full reopened a closed History: full=%+v detail=%q", a.contactHistoryFull, a.activeDetail())
	}
}

func TestHistoryFullActiveRequestPreservesInlineCarrier(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	a.openDetail(detailHistory)
	a.aux = "task-context"
	a.composerPanel = composerPanelContext
	a.taskContextIssueID = ""
	a.contactHistoryDetailBusy = true
	a.applyContactHistoryFull(nil, a.connectionGeneration, ct.ID, a.contactHistoryGen, a.contactHistoryDetailGen,
		&contactHistoryFull{SessionID: "s1", EntryID: "h1", Text: "full with inline context"}, "", nil)
	if a.contactHistoryFull == nil || a.aux != "task-context" || !a.composerInlineAux() {
		t.Fatalf("inline carrier was lost: full=%+v aux=%q panel=%q", a.contactHistoryFull, a.aux, a.composerPanel)
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("full with inline context") || !tr.Focused("Close full message") {
		t.Fatalf("inline full reading page = focused=%v texts=%v", tr.Focused("Close full message"), tr.Texts())
	}
	if err := tr.Click("← Back to history"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.contactHistoryFull != nil || !a.detailIs(detailHistory) || a.aux != "task-context" || !a.composerInlineAux() {
		t.Fatalf("Back lost inline carrier: full=%+v detail=%q aux=%q panel=%q",
			a.contactHistoryFull, a.activeDetail(), a.aux, a.composerPanel)
	}
}

func TestHistoryFullLateResultCannotHijackReopenedInlineKind(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	a.openDetail(detailHistory)
	a.aux = "model"
	a.composerPanel = composerPanelModel
	searchGen := a.contactHistoryGen
	detailGen := a.contactHistoryDetailGen
	a.contactHistoryDetailBusy = true
	a.openComposerModel()
	a.applyContactHistoryFull(nil, a.connectionGeneration, ct.ID, searchGen, detailGen,
		&contactHistoryFull{SessionID: "s1", EntryID: "h1", Text: "late same kind"}, "", nil)
	if a.contactHistoryFull != nil || !a.composerInlineAux() || a.contactHistoryDetailBusy {
		t.Fatalf("late result stole reopened inline kind: full=%+v aux=%q panel=%q busy=%v",
			a.contactHistoryFull, a.aux, a.composerPanel, a.contactHistoryDetailBusy)
	}
}

func TestHistoryFullRefusedInlineOpenDoesNotInvalidatePendingRead(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	a.openDetail(detailHistory)
	a.aux = "model"
	a.composerPanel = composerPanelModel
	searchGen := a.contactHistoryGen
	detailGen := a.contactHistoryDetailGen
	a.contactHistoryDetailBusy = true
	a.modelOpActive = true
	a.openComposerModel()
	a.applyContactHistoryFull(nil, a.connectionGeneration, ct.ID, searchGen, detailGen,
		&contactHistoryFull{SessionID: "s1", EntryID: "h1", Text: "accepted after refusal"}, "", nil)
	if a.contactHistoryFull == nil || a.contactHistoryFull.Text != "accepted after refusal" || !a.composerInlineAux() {
		t.Fatalf("refused inline open invalidated pending read: full=%+v aux=%q panel=%q",
			a.contactHistoryFull, a.aux, a.composerPanel)
	}
}

func TestHistoryFullActiveRequestUsesMainReadingPage(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	a.openDetail(detailHistory)
	a.contactHistoryDetailBusy = true
	a.applyContactHistoryFull(nil, a.connectionGeneration, ct.ID, a.contactHistoryGen, a.contactHistoryDetailGen,
		&contactHistoryFull{SessionID: "s1", EntryID: "h1", Text: "full marker"}, "", nil)
	if a.contactHistoryFull == nil || !a.mainPageActive() {
		t.Fatalf("active full message did not open the main page: %+v", a.contactHistoryFull)
	}
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("full marker") || !tr.Focused("Close full message") {
		t.Fatalf("full reading page = focused=%v texts=%v", tr.Focused("Close full message"), tr.Texts())
	}
	if err := tr.Click("← Back to history"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.contactHistoryFull != nil || !a.detailIs(detailHistory) {
		t.Fatalf("Back did not return to History: full=%+v detail=%q", a.contactHistoryFull, a.activeDetail())
	}
}

func TestStopConversationKeepsVisibleTextAndModelScope(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.SessionID = "s1"
	ct.SessionModel = "gpt-fixture"
	ct.ActiveTaskID = "task-live"
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.HasText("Stop conversation") || !tr.HasText("Conversation") || !tr.HasText("gpt-fixture") {
		t.Fatalf("stop/model scope missing: %v", tr.Texts())
	}
	rect, ok := tr.Find("Stop conversation")
	if !ok || rect.W < 80 || rect.H < 32 {
		t.Fatalf("stop control geometry = %+v/%v", rect, ok)
	}
	for _, text := range tr.Texts() {
		if strings.Contains(text, "More") {
			t.Fatalf("primary action overflowed into toolbar menu: %q", text)
		}
	}
}

func TestSourcesPageBackReturnsToOriginatingControl(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Messages = []*message{{ID: 7, Kind: msgContact, Text: "answer", Expanded: true, Sources: []string{"h1"}}}
	ct.History = []historyEntry{{ID: "h1", Date: "Yesterday", From: "contact", Text: "source text"}}
	a.openSources(7)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !a.mainPageActive() || !tr.HasText("source text") || tr.HasText("Composer for Saki") {
		t.Fatalf("sources did not replace the main reading surface: %v", tr.Texts())
	}
	if err := tr.Click("← Back"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.aux != "" || a.mainPageActive() || !tr.Focused("1 earlier messages") {
		t.Fatalf("sources Back = aux=%q focused=%v", a.aux, tr.Focused("1 earlier messages"))
	}
}

func TestHistoryQueryAndScrollStayPerContact(t *testing.T) {
	a, first := workspaceTestApp(t)
	a.windowWidth = 1040
	second := &contact{ID: "other", Name: "Other", Role: "Assistant", SessionID: "s2", Loaded: true}
	a.contacts = append(a.contacts, second)
	for i := 0; i < 30; i++ {
		first.History = append(first.History, historyEntry{ID: fmt.Sprintf("a-%02d", i), Date: "Today", From: "contact", Text: fmt.Sprintf("alpha %02d", i)})
		second.History = append(second.History, historyEntry{ID: fmt.Sprintf("b-%02d", i), Date: "Today", From: "contact", Text: fmt.Sprintf("beta %02d", i)})
	}
	a.openHistory()
	a.historyQuery = "alpha"
	a.historyScroll.Y = 41
	a.selectContact(1)
	if a.historyQuery != "" || a.historyScroll.Y != 0 {
		t.Fatalf("contact switch leaked A history: query=%q scroll=%v", a.historyQuery, a.historyScroll.Y)
	}
	a.historyQuery = "beta"
	a.historyScroll.Y = 17
	a.selectContact(0)
	if a.historyQuery != "alpha" || a.historyScroll.Y != 41 {
		t.Fatalf("A history not restored: query=%q scroll=%v", a.historyQuery, a.historyScroll.Y)
	}
	a.selectContact(1)
	if a.historyQuery != "beta" || a.historyScroll.Y != 17 {
		t.Fatalf("B history not restored: query=%q scroll=%v", a.historyQuery, a.historyScroll.Y)
	}
}

func TestNavigationToolbarArrowFocusStaysInsideVisibleControls(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.windowWidth = 1040
	a.workspaceOpen = false
	a.detail = ""
	a.focusWorkspace = true
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if !tr.Focused("Work") {
		t.Fatal("toolbar did not take Work focus")
	}
	tr.Key(0, ui.KeyRight)
	tr.Frame()
	if !tr.Focused("History") {
		t.Fatalf("Right did not move to History: %v", tr.Texts())
	}
	tr.Key(0, ui.KeyRight)
	tr.Frame()
	if !tr.Focused("Contact profile") {
		t.Fatal("Right did not move to Contact profile")
	}
}

func TestProfileAndSourcesSettingsReturnToVisibleOrigin(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	a.workspaceOpen = false
	a.detail = ""
	a.openDetail(detailProfile)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	tr.Key(ui.Cmd, ui.KeyComma)
	tr.Frame()
	if !a.settingsOpen {
		t.Fatal("Cmd+, did not open Settings from profile")
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.detailIs(detailProfile) || !tr.Focused("Edit full profile") {
		t.Fatalf("profile settings return did not focus its visible Edit: %v", tr.Texts())
	}

	ct.Messages = []*message{{ID: 9, Kind: msgContact, Text: "sources", Sources: []string{"h1"}}}
	a.openSources(9)
	tr.Frame()
	tr.Key(ui.Cmd, ui.KeyComma)
	tr.Frame()
	if !a.settingsOpen {
		t.Fatal("Cmd+, did not open Settings from sources")
	}
	if err := tr.Click("← Back to workbench"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.aux != "sources" || !tr.Focused("Close sources") {
		t.Fatalf("sources settings return = aux=%q focused=%v", a.aux, tr.Focused("Close sources"))
	}
}

func TestIssueModelPageBackReturnsToItsIssueButton(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	a.workspaceOpen = false
	a.detail = ""
	issue := &productIssue{ID: "i-model", ContactID: ct.ID, Goal: "model issue", WorkSessionID: "work-model", Status: issueStatusInProgress}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	nav := ct.issueNavigation()
	nav.DetailID = issue.ID
	a.issueDetailOpen = map[string]bool{issue.ID: true}
	a.openDetail(detailWork)
	a.openSessionModel(issue.WorkSessionID, "work", issue.Goal)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if a.aux != "model" || !a.mainPageActive() || !tr.Focused("Close session model") {
		t.Fatalf("issue model page = aux=%q focused=%v", a.aux, tr.Focused("Close session model"))
	}
	if err := tr.Click("← Back"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.aux != "" || !a.detailIs(detailWork) || !tr.Focused("Issue model") {
		t.Fatalf("issue model Back = aux=%q detail=%q focused=%v", a.aux, a.activeDetail(), tr.Focused("Issue model"))
	}
}

func TestIssueContextPageBackReturnsToItsIssueButton(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	a.workspaceOpen = false
	a.detail = ""
	issue := &productIssue{ID: "i-context", ContactID: ct.ID, Goal: "context issue", Status: issueStatusInProgress}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	nav := ct.issueNavigation()
	nav.DetailID = issue.ID
	a.issueDetailOpen = map[string]bool{issue.ID: true}
	a.openDetail(detailWork)
	a.openIssueTaskContext(issue.ID)
	tr := ui.NewTester(a.view, 1040, 720)
	tr.Frame()
	if a.aux != "task-context" || !tr.Focused("Close task context") {
		t.Fatalf("issue context page = aux=%q focused=%v", a.aux, tr.Focused("Close task context"))
	}
	if err := tr.Click("← Back"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if a.aux != "" || !a.detailIs(detailWork) || !tr.Focused("Task context") {
		t.Fatalf("issue context Back = aux=%q detail=%q focused=%v", a.aux, a.activeDetail(), tr.Focused("Task context"))
	}
}

func navigationPreviewApp(t *testing.T) (*appModel, *contact) {
	t.Helper()
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	ct.Name = "Saki"
	ct.Role = "Research partner"
	ct.Purpose = "Keep product decisions and evidence together across long conversations."
	ct.Instructions = "Read the current state before changing it.\nKeep the source and return path visible."
	for i := 0; i < 28; i++ {
		ct.History = append(ct.History, historyEntry{
			ID: fmt.Sprintf("preview-history-%02d", i), Date: "Oct 30", From: "contact",
			Text: fmt.Sprintf("预算与发布证据 %02d：这是一段足够长的中文历史内容，用来检查右侧列表的密度、换行和滚动位置。", i),
		})
	}
	issue := &productIssue{
		ID: "preview-issue", ContactID: ct.ID, Goal: "整理发布证据与未完成事项",
		Status: issueStatusInProgress, RequirementsRevision: 1,
	}
	for i := 0; i < 8; i++ {
		issue.Requirements = append(issue.Requirements, &issueRequirement{
			ID: fmt.Sprintf("preview-r-%d", i), Text: fmt.Sprintf("发布前核对第 %d 项证据与回滚说明", i),
			State: requirementUnverified,
		})
	}
	ct.Issues = []*productIssue{issue}
	ct.SelectedIssueID = issue.ID
	ct.SelectedWorkTaskID = ""
	ct.Draft = "保留这段未发送的中文草稿。"
	return a, ct
}

func TestPreviewNavigation219(t *testing.T) {
	dir := os.Getenv("SAKI_PREVIEW_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := loadAvatar(); err != nil {
		t.Fatal(err)
	}
	for _, dark := range []bool{false, true} {
		theme := "light"
		if dark {
			theme = "dark"
		}
		for _, width := range []int{1040, 720} {
			height := 720
			if width == 720 {
				height = 640
			}
			for _, kind := range []detailKind{detailWork, detailHistory, detailProfile} {
				a, ct := navigationPreviewApp(t)
				a.windowWidth = width
				a.workspaceOpen = false
				a.detail = ""
				switch kind {
				case detailWork:
					nav := ct.issueNavigation()
					if width == 1040 {
						nav.DetailID = "preview-issue"
						nav.DetailScroll.Y = 60
					}
					a.openDetail(detailWork)
				case detailHistory:
					a.historyQuery = "预算"
					a.historyScroll.Y = 36
					a.openDetail(detailHistory)
				case detailProfile:
					a.openDetail(detailProfile)
				}
				tr := ui.NewTester(a.view, width, height)
				tr.SetDark(dark)
				tr.Frame()
				if !a.detailIs(kind) {
					t.Fatalf("%s preview carrier = %q", kind, a.activeDetail())
				}
				switch kind {
				case detailWork:
					if width == 1040 {
						if !tr.HasText("整理发布证据与未完成事项") || !tr.HasText("Requirements") {
							t.Fatalf("work preview lacks real issue content: %v", tr.Texts())
						}
					} else if !tr.HasText("New issue") || !tr.HasText("Issue filter") || !tr.HasText("Conversation work") {
						t.Fatalf("narrow work preview lost creation/overview content: %v", tr.Texts())
					}
				case detailHistory:
					if !tr.HasText("History · Saki") || !tr.HasText("预算与发布证据") {
						t.Fatalf("history preview lacks real search/list content: %v", tr.Texts())
					}
				case detailProfile:
					if !tr.HasText("Contact profile") || !tr.HasText("Keep product decisions") {
						t.Fatalf("profile preview lacks current contact facts: %v", tr.Texts())
					}
				}
				if width == 720 {
					if tr.HasText("Composer for Saki") || tr.HasText("Add contact") || !tr.HasText("Back") {
						t.Fatalf("%s narrow preview is not the application page: %v", kind, tr.Texts())
					}
				}
				writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("navigation-%s-%s-%d.png", kind, theme, width)), tr.Image())
			}

			a, ct := navigationPreviewApp(t)
			a.windowWidth = width
			file := issueResultPanelFixtureFile("preview", "/tmp/workspace/preview-result.md", `{"path":"/tmp/workspace/preview-result.md","bytes_written":1}`)
			a.aux = "issue-result"
			a.issueResult = issueResultPage{
				Open: true, OwnerContactID: ct.ID, IssueID: "preview-issue", ExecutionID: "preview-exec",
				Panel: issueResultPanelState{
					RunLabel: "Run 1 · 整理发布证据与未完成事项", RunStatus: "Completed",
					Response: "# 发布检查结果\n\n内容优先的主区阅读页保留完整中文正文，**不挤进 340px 右侧详情**。\n\n- file → result → work 逐层返回\n- Settings 返回到当前入口\n\n```text\ncurrent-file-preview\n```",
					Files:    []issueResultFile{file},
				},
			}
			tr := ui.NewTester(a.view, width, height)
			tr.SetDark(dark)
			tr.Frame()
			if !a.mainPageActive() || !tr.HasText("Run results") || !tr.HasText("发布检查结果") || !tr.HasText("Read file 1: preview-result.md") {
				t.Fatalf("result preview lacks real main-page body: %v", tr.Texts())
			}
			if width == 720 && (tr.HasText("Composer for Saki") || tr.HasText("Add contact")) {
				t.Fatalf("narrow result preview mounted the workbench background")
			}
			writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("navigation-result-%s-%d.png", theme, width)), tr.Image())

			key := issueResultFileKey(file)
			inspection := issueResultFileInspection{
				Path: file.Path, WorkingDirectory: file.WorkingDirectory, Exists: true, Regular: true,
				Preview: "# 当前文件预览\n\n这是文件阅读页的**长中文正文**，用来核对主区宽度、行距与返回入口。\n\n- 保留来源与执行身份\n- 代码块和链接仍可阅读\n\n[查看来源](https://example.com/preview-source)\n\n```go\nfunc preview() string {\n\treturn \"完整正文在主区阅读\"\n}\n```\n",
			}
			a.issueResult.Panel.Selected = key
			a.issueResult.Panel.Inspection = &inspection
			a.issueResult.Panel.InspectFor = key
			a.issueResult.Panel.ReaderKey = key
			a.issueResult.Panel.FocusReader = true
			tr.Frame()
			if !tr.HasText("File reading") || !tr.HasText("当前文件预览") || !tr.HasText("完整正文在主区阅读") {
				t.Fatalf("reader preview lacks inspected file body: %v", tr.Texts())
			}
			if width == 720 && (tr.HasText("Composer for Saki") || tr.HasText("Add contact")) {
				t.Fatalf("narrow reader preview mounted the workbench background")
			}
			writePreviewPNG(t, filepath.Join(dir, fmt.Sprintf("navigation-reader-%s-%d.png", theme, width)), tr.Image())
		}
	}
}

func TestHistoryDuplicateEntryReturnKeepsClickedOrigin(t *testing.T) {
	a, ct := workspaceTestApp(t)
	a.windowWidth = 1040
	a.workspaceOpen = false
	a.detail = ""
	ct.History = []historyEntry{{ID: "same-entry", Date: "Today", From: "contact", Text: "Loaded history text"}}
	a.contactHistory = &contactHistoryResult{Matches: []contactHistoryMatch{{
		SessionID: ct.SessionID, EntryID: "same-entry", Date: "Today", Preview: "Saved search text",
	}}}
	a.openHistory()
	tr := ui.NewTester(a.view, 1040, 900)
	tr.Frame()
	if err := tr.Click("Read full message"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !a.mainPageActive() || a.contactHistoryFull == nil || a.contactHistoryFull.Origin != historyOriginLoaded {
		t.Fatalf("loaded entry did not open its own reading page: %+v", a.contactHistoryFull)
	}
	if err := tr.Click("← Back to history"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	if !tr.Focused("Read full message") {
		t.Fatalf("return focused saved=%v loaded=%v; expected the clicked loaded entry",
			tr.Focused("View full message"), tr.Focused("Read full message"))
	}
	if tr.Focused("View full message") {
		t.Fatal("return also focused the saved search entry")
	}
}

func TestWorkSettingsReturnUsesVisibleCloseOrWorkEntry(t *testing.T) {
	for _, width := range []int{1040, 720} {
		a, _ := workspaceTestApp(t)
		a.windowWidth = width
		a.workspaceOpen = false
		a.detail = ""
		a.openDetail(detailWork)
		height := 720
		if width == 720 {
			height = 640
		}
		tr := ui.NewTester(a.view, width, height)
		tr.Frame()
		tr.Key(ui.Cmd, ui.KeyComma)
		tr.Frame()
		if !a.settingsOpen {
			t.Fatalf("width %d Cmd+, did not open Settings from Work", width)
		}
		if err := tr.Click("← Back to workbench"); err != nil {
			t.Fatal(err)
		}
		tr.Frame()
		if width == 720 {
			if !a.detailIs(detailWork) || !tr.Focused("Close work") {
				t.Fatalf("narrow Work settings return did not focus visible Back")
			}
		} else if !tr.Focused("Work") {
			t.Fatalf("wide Work settings return did not focus the Work entry")
		}
	}
}

func TestResultMainPageKeepsWorkbenchOutOfTabPath(t *testing.T) {
	a, ct := workspaceTestApp(t)
	ct.Draft = "draft remains"
	a.windowWidth = 720
	a.workspaceOpen = false
	a.detail = ""
	a.aux = "issue-result"
	a.issueResult = issueResultPage{
		Open: true, OwnerContactID: ct.ID, IssueID: "i-result", ExecutionID: "e-result",
		Panel: issueResultPanelState{RunLabel: "Run 1", RunStatus: "Completed", Response: "main page result body"},
	}
	tr := ui.NewTester(a.view, 720, 640)
	tr.Frame()
	if !a.mainPageActive() || tr.HasText("Composer for Saki") || tr.HasText("Add contact") {
		t.Fatalf("result did not replace the workbench: %v", tr.Texts())
	}
	for i := 0; i < 5; i++ {
		tr.Key(0, ui.KeyTab)
		tr.Frame()
		if tr.Focused("Composer for Saki") || tr.Focused("Add contact") {
			t.Fatal("result Tab reached an unmounted workbench control")
		}
	}
	if ct.Draft != "draft remains" {
		t.Fatalf("result page changed draft: %q", ct.Draft)
	}
}

func TestNarrowWorkKeepsNewIssueAction(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.workspaceOpen = false
	a.detail = ""
	a.windowWidth = 720
	tr := ui.NewTester(a.view, 720, 640)
	tr.Frame()
	if err := tr.Click("Work"); err != nil {
		t.Fatal(err)
	}
	tr.Frame()
	rect, ok := tr.Find("New issue")
	if !ok || rect.W <= 0 || rect.H <= 0 || rect.X < 0 || rect.Y < 0 || rect.X+rect.W > 720 || rect.Y+rect.H > 640 {
		t.Fatalf("narrow Work New issue = %+v/%v; texts=%v", rect, ok, tr.Texts())
	}
	if err := tr.Click("New issue"); err != nil {
		t.Fatalf("narrow Work lost creation action: %v", err)
	}
	tr.Frame()
	if !a.issueFormOpen || !tr.HasText("Issue goal") {
		t.Fatalf("New issue did not open the existing form: open=%v texts=%v", a.issueFormOpen, tr.Texts())
	}
}

func TestExplicitDetailOpenKeepsTheSamePageOpen(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.workspaceOpen = false
	a.detail = ""
	a.openDetail(detailHistory)
	a.openHistory()
	if !a.detailIs(detailHistory) {
		t.Fatalf("explicit History open closed or changed the page: %q", a.activeDetail())
	}
	a.toggleDetail(detailHistory)
	if a.workspaceOpen {
		t.Fatal("header toggle did not use the same-item close path")
	}
}

func TestAcceptedIssueModelTargetLeavesConversationCarrier(t *testing.T) {
	a, _ := workspaceTestApp(t)
	a.openComposerModel()
	if !a.composerInlineAux() || a.composerPanel != composerPanelModel {
		t.Fatalf("conversation model marker = aux=%q panel=%q", a.aux, a.composerPanel)
	}
	if !a.openSessionModel("issue-session", "work", "Issue") {
		t.Fatal("issue model rejected without an active operation")
	}
	if a.aux != "model" || a.composerPanel != "" || a.composerInlineAux() || !a.mainPageActive() || a.modelPanelKind != "work" {
		t.Fatalf("issue model kept conversation carrier: aux=%q panel=%q inline=%v main=%v kind=%q",
			a.aux, a.composerPanel, a.composerInlineAux(), a.mainPageActive(), a.modelPanelKind)
	}

	rejected, _ := workspaceTestApp(t)
	rejected.openComposerModel()
	rejected.modelOpActive = true
	if rejected.openSessionModel("issue-session", "work", "Issue") {
		t.Fatal("guarded issue model open was accepted")
	}
	if rejected.aux != "model" || rejected.composerPanel != composerPanelModel || !rejected.composerInlineAux() || rejected.modelPanelKind != "conversation" {
		t.Fatalf("rejected open changed the conversation carrier: aux=%q panel=%q inline=%v kind=%q",
			rejected.aux, rejected.composerPanel, rejected.composerInlineAux(), rejected.modelPanelKind)
	}
}
