package main

import "github.com/egoist/mygo/ui"

// workbenchTheme is the one per-frame resolution of the Paper & Moss
// appearance. Theme keeps the framework's control roles semantic; the small
// app-only set names reading surfaces that must not be reused as button faces.
type workbenchTheme struct {
	Theme         *ui.Theme
	Rail          ui.Color
	Reading       ui.Color
	Panel         ui.Color
	Well          ui.Color
	BubbleUser    ui.Color
	BubbleContact ui.Color
	WorkSurface   ui.Color
	WorkText      ui.Color
	WorkMuted     ui.Color
	WorkAttention ui.Color
	WorkWarningBg ui.Color
}

func resolveWorkbenchTheme(dark bool) workbenchTheme {
	t := ui.LightTheme()
	t.FontSize = 14
	t.Spacing = 4
	t.Radius = 8
	if dark {
		t = ui.DarkTheme()
		t.FontSize = 14
		t.Spacing = 4
		t.Radius = 8
		t.Background = ui.Hex("#202622")
		t.Surface = ui.Hex("#2a332d")
		t.SurfaceHover = ui.Hex("#33473a")
		t.SurfacePressed = ui.Hex("#3b5042")
		t.Border = ui.Hex("#3b473f")
		t.Text = ui.Hex("#edf1eb")
		t.TextMuted = ui.Hex("#adb8af")
		t.Accent = ui.Hex("#b7d1bd")
		t.AccentHover = ui.Hex("#c9dfcd")
		t.AccentPressed = ui.Hex("#d6e8d6")
		t.AccentText = ui.Hex("#203328")
		t.Selection = ui.Hex("#33473a")
		t.Focus = ui.Hex("#b7d1bd")
		t.Danger = ui.Hex("#f0a89a")
		t.Warning = ui.Hex("#edbb8c")
		t.Success = ui.Hex("#b7d1bd")
		t.Scrollbar = ui.Hex("#7e8c82")
		return workbenchTheme{
			Theme: t,
			Rail:  ui.Hex("#1a201c"), Reading: ui.Hex("#202622"), Panel: ui.Hex("#1d231f"),
			Well: ui.Hex("#2a332d"), BubbleUser: ui.Hex("#33473a"), BubbleContact: ui.Hex("#2a332d"),
			WorkSurface: ui.Hex("#1d231f"), WorkText: ui.Hex("#edf1eb"),
			WorkMuted: ui.Hex("#adb8af"), WorkAttention: ui.Hex("#edbb8c"),
			WorkWarningBg: ui.Hex("#3b3027"),
		}
	}
	t.Background = ui.Hex("#ffffff")
	t.Surface = ui.Hex("#eff2f0")
	t.SurfaceHover = ui.Hex("#e8f0ea")
	t.SurfacePressed = ui.Hex("#dce7df")
	t.Border = ui.Hex("#dde3df")
	t.Text = ui.Hex("#252c28")
	t.TextMuted = ui.Hex("#626c65")
	t.Accent = ui.Hex("#365e4d")
	t.AccentHover = ui.Hex("#2d5343")
	t.AccentPressed = ui.Hex("#244437")
	t.AccentText = ui.Hex("#ffffff")
	t.Selection = ui.Hex("#e8f0ea")
	t.Focus = ui.Hex("#437762")
	t.Danger = ui.Hex("#9c4a62")
	t.Warning = ui.Hex("#8b542d")
	t.Success = ui.Hex("#3d6a52")
	t.Scrollbar = ui.Hex("#aeb8b1")
	return workbenchTheme{
		Theme: t,
		Rail:  ui.Hex("#f5f6f5"), Reading: ui.Hex("#ffffff"), Panel: ui.Hex("#f8f9f8"),
		Well: ui.Hex("#eff2f0"), BubbleUser: ui.Hex("#e8f0ea"), BubbleContact: ui.Hex("#f2f4f2"),
		WorkSurface: ui.Hex("#f8f9f8"), WorkText: ui.Hex("#252c28"),
		WorkMuted: ui.Hex("#626c65"), WorkAttention: ui.Hex("#8b542d"),
		WorkWarningBg: ui.Hex("#f7ede3"),
	}
}

// secondaryButton is the visible secondary-action face; PrimaryButton stays
// reserved for the main action. Do not set Background on this control: MyGo's
// styleButton only supplies hover/pressed while its default face is intact.
func secondaryButton(c *ui.Context, label string) ui.Element {
	return ui.Button(c, label).FontSize(13).FontWeight(500).MinHeight(32).Padding(6, 10)
}
