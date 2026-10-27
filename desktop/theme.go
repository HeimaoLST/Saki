package main

import "github.com/egoist/mygo/ui"

// workbenchTheme is the one per-frame resolution of the current Paper & Moss
// reference. The framework theme carries shared widget roles; the small
// app-only set carries the workbench roles that ui.Theme does not define.
type workbenchTheme struct {
	Theme         *ui.Theme
	Rail          ui.Color
	BubbleUser    ui.Color
	WorkSurface   ui.Color
	WorkText      ui.Color
	WorkMuted     ui.Color
	WorkAttention ui.Color
}

func resolveWorkbenchTheme(dark bool) workbenchTheme {
	t := ui.LightTheme()
	t.FontSize = 14
	t.Spacing = 4
	t.Radius = 10
	if dark {
		t = ui.DarkTheme()
		t.FontSize = 14
		t.Spacing = 4
		t.Radius = 10
		t.Background = ui.Hex("#1d2823")
		t.Surface = ui.Hex("#222f28")
		t.SurfaceHover = ui.Hex("#344238")
		t.SurfacePressed = ui.Hex("#344238")
		t.Border = ui.Hex("#3b493d")
		t.Text = ui.Hex("#e9ebdc")
		t.TextMuted = ui.Hex("#aab5a5")
		t.Accent = ui.Hex("#c7d7ac")
		t.AccentHover = ui.Hex("#dce8c5")
		t.AccentPressed = ui.Hex("#dce8c5")
		t.AccentText = ui.Hex("#1d3025")
		t.Selection = ui.Hex("#344238")
		t.Focus = ui.Hex("#e3b18b")
		t.Danger = ui.Hex("#f0a89a")
		t.Warning = ui.Hex("#e2b48c")
		t.Success = ui.Hex("#b3cea5")
		t.Scrollbar = ui.Hex("#aab5a5")
		return workbenchTheme{
			Theme: t, Rail: ui.Hex("#18221c"), BubbleUser: ui.Hex("#344236"),
			WorkSurface: ui.Hex("#354e3b"), WorkText: ui.Hex("#f4f4df"),
			WorkMuted: ui.Hex("#ced9bd"), WorkAttention: ui.Hex("#f2c2a0"),
		}
	}
	t.Background = ui.Hex("#fbfaf6")
	t.Surface = ui.Hex("#f4f2eb")
	t.SurfaceHover = ui.Hex("#e9e8df")
	t.SurfacePressed = ui.Hex("#e9e8df")
	t.Border = ui.Hex("#deded3")
	t.Text = ui.Hex("#263c34")
	t.TextMuted = ui.Hex("#606b62")
	t.Accent = ui.Hex("#31594a")
	t.AccentHover = ui.Hex("#26493c")
	t.AccentPressed = ui.Hex("#26493c")
	t.AccentText = ui.Hex("#fffdf5")
	t.Selection = ui.Hex("#e9e8df")
	t.Focus = ui.Hex("#a55939")
	t.Danger = ui.Hex("#a13f39")
	t.Warning = ui.Hex("#965331")
	t.Success = ui.Hex("#38674f")
	t.Scrollbar = ui.Hex("#606b62")
	return workbenchTheme{
		Theme: t, Rail: ui.Hex("#eeeee5"), BubbleUser: ui.Hex("#e5ebdf"),
		WorkSurface: ui.Hex("#294a3d"), WorkText: ui.Hex("#fbf9e9"),
		WorkMuted: ui.Hex("#c9d5bd"), WorkAttention: ui.Hex("#f2c2a0"),
	}
}

// secondaryButton is the visible secondary-action face; PrimaryButton stays reserved for the main action.
func secondaryButton(c *ui.Context, label string) ui.Element {
	return ui.Button(c, label).FontSize(13).MinHeight(32).Padding(6, 10)
}
