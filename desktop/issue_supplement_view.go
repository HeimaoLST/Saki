package main

// Supplement UI consumes the same productIssue/ledger facts as the automatic
// path. Every callback mutates only the app model; it never claims model
// adoption or requirement satisfaction.

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

func (a *appModel) issueSupplementSection(c *ui.Context, t *ui.Theme, ct *contact, issue *productIssue, editable bool) {
	ledger := issue.Supplements
	auto := ledger == nil || ledger.AutoContinue
	pending := 0
	batches := []*issueSupplementBatch(nil)
	if ledger != nil {
		pending = ledger.pendingCount()
		batches = ledger.Batches
	}
	target := issueSupplementTargetFor(ct, issue)
	if target.IssueID == "" {
		return
	}
	formOpen := a.issueSupplementFormID == issue.ID
	if pending == 0 && len(batches) == 0 && !formOpen {
		return
	}
	ui.Divider(c)
	ui.Row(c).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "Supplements").FontSize(12).FontWeight(600).TextColor(t.Text)
		if pending > 0 {
			ui.Text(c, fmt.Sprintf("%d pending", pending)).FontSize(10).TextColor(t.Warning)
		}
		ui.Spacer(c)
		if auto {
			ui.Text(c, "Automatic continuation on").FontSize(10).TextColor(t.TextMuted)
		} else {
			ui.Text(c, "Automatic continuation stopped").FontSize(10).TextColor(t.Warning)
		}
	})
	ui.Text(c, fmt.Sprintf("Current target · revision %d · %s", issue.RequirementsRevision, truncate(issue.Goal, 100))).
		FontSize(10).LineHeight(1.35).TextColor(t.TextMuted)
	if ledger != nil {
		if active := ledger.activeBatch(target); active != nil {
			switch active.State {
			case supplementBatchDispatching:
				ui.Text(c, "Dispatching; waiting for the Runtime receipt. Automatic continuation will not start another batch yet.").
					FontSize(10).LineHeight(1.35).TextColor(t.Warning)
			case supplementBatchUnknown:
				ui.Text(c, "Delivery is unknown; check the execution. Nothing will be resent automatically.").
					FontSize(10).LineHeight(1.35).TextColor(t.Warning)
			}
		} else if pending > 0 && auto {
			if reason := a.canStartIssue(ct, issue); reason != "" {
				ui.Text(c, "Waiting: "+reason).FontSize(10).LineHeight(1.35).TextColor(t.TextMuted)
			} else if len(issue.Executions) == 0 {
				ui.Text(c, "No execution yet. Use Start with supplement to authorize the first run.").
					FontSize(10).LineHeight(1.35).TextColor(t.TextMuted)
			} else {
				ui.Text(c, "Ready at the next safe boundary.").FontSize(10).TextColor(t.TextMuted)
			}
		} else if pending > 0 {
			ui.Text(c, "Pending text is preserved. Resume automatic continuation or use Continue explicitly.").
				FontSize(10).LineHeight(1.35).TextColor(t.TextMuted)
		}
	}
	if ledger != nil {
		var pendingItems []*issueSupplement
		for _, item := range ledger.Items {
			if item != nil && item.State == supplementStatePending {
				pendingItems = append(pendingItems, item)
			}
		}
		if len(pendingItems) > 0 {
			ui.Text(c, "Saved pending supplements").FontSize(10).FontWeight(500).TextColor(t.TextMuted)
			for _, item := range pendingItems {
				item := item
				ui.Column(c).Padding(6).Radius(6).Background(t.Surface).Gap(2).Children(func() {
					ui.Text(c, fmt.Sprintf("received r%d · %s", item.RequirementsRevision, truncate(item.Text, 180))).
						FontSize(10).LineHeight(1.35).TextColor(t.Text)
					if a.issueSupplementPreviewID == item.ID {
						ui.Row(c).Children(func() {
							ui.Button(c, "Close preview").FontSize(9).Padding(1, 0).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { a.issueSupplementPreviewID = "" })
						})
						markdownReader(c, item.Text, markdownReaderOptions{PlainText: true, CopyLabel: "Copy supplement text"}).
							FontSize(10).LineHeight(1.35).TextColor(t.Text)
					} else {
						ui.Button(c, "Preview full text").FontSize(9).Padding(1, 0).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { a.issueSupplementPreviewID = item.ID })
					}
				})
			}
		}
	}
	if editable {
		if !formOpen {
			ui.Row(c).Gap(6).Children(func() {
				ui.Button(c, "Add supplement").FontSize(10).Padding(2, 0).Background(ui.Transparent).BorderWidth(0).TextColor(t.Text).OnClick(func() {
					a.openIssueSupplementForm(issue.ID)
				})
			})
		} else {
			draft := a.issueSupplementDraft(issue.ID)
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Add supplement").FontSize(10).FontWeight(500).TextColor(t.TextMuted)
				ui.Spacer(c)
				ui.Button(c, "Cancel").FontSize(10).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() {
					a.closeIssueSupplementForm()
				})
			})
			ui.TextArea(c, draft).Label("Add supplement to this issue").FontSize(12).Height(56).
				Placeholder("Supplementary target, requirement, or correction")
			ui.Row(c).Gap(6).Children(func() {
				ui.PrimaryButton(c, "Save supplement").FontSize(11).OnClick(func() {
					if !a.saveIssueSupplement(ct, issue.ID, *draft) {
						return
					}
					*draft = ""
					a.closeIssueSupplementForm()
				})
			})
			ui.Text(c, "Saving a supplement is not a model-acceptance or requirement-satisfaction receipt.").
				FontSize(10).LineHeight(1.35).TextColor(t.TextMuted)
		}
		if ledger != nil {
			if auto {
				ui.Button(c, "Stop auto").FontSize(10).Padding(2, 0).Background(ui.Transparent).BorderWidth(0).TextColor(t.Warning).OnClick(func() {
					if !a.setIssueSupplementAuto(ct, issue.ID, false) {
						a.issueActionError = "Could not save the automatic-continuation stop."
					}
				})
			} else if pending > 0 {
				ui.Button(c, "Resume auto").FontSize(10).Padding(2, 0).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() {
					if !a.setIssueSupplementAuto(ct, issue.ID, true) {
						a.issueActionError = "Could not save the automatic-continuation resume."
					}
				})
			}
		}
	}
	if len(batches) > 0 {
		start := 0
		if !a.issueSupplementShowAllBatches && len(batches) > 6 {
			start = len(batches) - 6
		}
		if start > 0 {
			ui.Text(c, fmt.Sprintf("%d earlier batches hidden from this card; the execution list remains the original input record.", start)).
				FontSize(9).LineHeight(1.3).TextColor(t.TextMuted)
		}
		for _, batch := range batches[start:] {
			if batch == nil {
				continue
			}
			batch := batch
			ui.Column(c).Padding(7).Radius(7).Background(t.Surface).Gap(2).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					ui.Text(c, issueSupplementBatchLabel(batch)).FontSize(10).FontWeight(500).TextColor(t.TextMuted)
					ui.Spacer(c)
					ui.Text(c, fmt.Sprintf("target r%d", batch.Target.RequirementsRevision)).
						FontSize(9).TextColor(t.TextMuted)
				})
				for _, item := range batch.Snapshot {
					item := item
					ui.Column(c).Gap(1).Children(func() {
						ui.Text(c, fmt.Sprintf("received r%d · %s", item.RequirementsRevision, truncate(item.Text, 180))).
							FontSize(10).LineHeight(1.35).TextColor(t.Text)
						if a.issueSupplementPreviewID == item.ID {
							ui.Button(c, "Close preview").FontSize(9).Padding(1, 0).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { a.issueSupplementPreviewID = "" })
							markdownReader(c, item.Text, markdownReaderOptions{PlainText: true, CopyLabel: "Copy supplement text"}).
								FontSize(10).LineHeight(1.35).TextColor(t.Text)
						} else {
							ui.Button(c, "Preview full text").FontSize(9).Padding(1, 0).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { a.issueSupplementPreviewID = item.ID })
						}
					})
				}
				if batch.SaveError != "" {
					ui.Text(c, "Local receipt save failed: "+batch.SaveError).FontSize(10).LineHeight(1.35).TextColor(t.Warning)
				}
				if batch.State == supplementBatchAccepted {
					ui.Text(c, "Submitted to Runtime execution · "+shortExecutionID(batch.ExecutionID)+" · task "+batch.TaskID).
						FontSize(9).TextColor(t.TextMuted)
				}
				if editable && (batch.State == supplementBatchRejected || batch.State == supplementBatchNotDispatched) {
					ui.Button(c, "Retry supplement").FontSize(10).Padding(2, 0).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() {
						if !a.retryIssueSupplementBatch(ct, issue, batch) {
							if a.issueActionError == "" {
								a.issueActionError = "Could not retry this supplement."
							}
						}
					})
				}
				if batch.State == supplementBatchUnknown || batch.State == supplementBatchDispatching {
					ui.Text(c, "Needs delivery check; this is not a completion claim.").FontSize(9).TextColor(t.Warning)
					if batch.ExecutionID != "" {
						executionID := batch.ExecutionID
						ui.Button(c, "Check execution").FontSize(10).Padding(2, 0).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() {
							if !a.reconcileIssueExecution(issue.ID, executionID) {
								a.issueActionError = "Could not check this supplement execution."
							}
						})
					}
				}
			})
		}
		if len(batches) > 6 {
			if a.issueSupplementShowAllBatches {
				ui.Button(c, "Show recent batches").FontSize(9).Padding(1, 0).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { a.issueSupplementShowAllBatches = false })
			} else {
				ui.Button(c, "Show all batches").FontSize(9).Padding(1, 0).Background(ui.Transparent).BorderWidth(0).TextColor(t.TextMuted).OnClick(func() { a.issueSupplementShowAllBatches = true })
			}
		}
	}
}

func shortExecutionID(id string) string {
	if len(id) <= 18 {
		return id
	}
	return id[:18] + "…"
}

func issueSupplementBatchLabel(batch *issueSupplementBatch) string {
	switch batch.State {
	case supplementBatchDispatching:
		return "Batch dispatching · receipt pending"
	case supplementBatchAccepted:
		return "Batch accepted by execution"
	case supplementBatchRejected:
		return "Batch rejected"
	case supplementBatchNotDispatched:
		return "Batch not dispatched"
	case supplementBatchUnknown:
		return "Batch unknown · check execution"
	case supplementBatchCanceled:
		return "Batch canceled / superseded"
	default:
		return "Batch " + batch.State
	}
}
