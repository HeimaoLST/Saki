package main

import "github.com/egoist/mygo/ui"

// workbenchTheme is the one per-frame resolution of the Paper & Moss
// appearance. Theme keeps the framework's control roles semantic; the small
// app-only set names reading surfaces that must not be reused as button faces.
// Composer and workbench type roles are shared by the existing render paths
// rather than a second design system. Body and metadata sizes keep the
// pre-existing 15px reading text and 12px supporting text.
const (
	typeContactTitle float32 = 20
	typePageTitle    float32 = 18
	typeGroupTitle   float32 = 14
	typeBody         float32 = 15
	typeAction       float32 = 13
	typeMeta         float32 = 12
	typeRailName     float32 = 14
	typeRailSummary  float32 = 13
)

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
		// #218 palette: the same theme fields, populated from the researched
		// semantic roles. Border is the control/input boundary; the decorative
		// line role has no separate Theme field in v0.7.3.
		t.Background = ui.Hex("#1D2520")
		t.Surface = ui.Hex("#29362D")
		t.SurfaceHover = ui.Hex("#314D3B")
		t.SurfacePressed = ui.Hex("#303B33")
		t.Border = ui.Hex("#879B8D")
		t.Text = ui.Hex("#EDF3EE")
		t.TextMuted = ui.Hex("#B0BFB3")
		t.Accent = ui.Hex("#B5D5BE")
		t.AccentHover = ui.Hex("#C7E3CF")
		t.AccentPressed = ui.Hex("#D8EDDE")
		t.AccentText = ui.Hex("#183624")
		t.Selection = ui.Hex("#314D3B")
		t.Focus = ui.Hex("#A7D6B8")
		t.Danger = ui.Hex("#F2A8AF")
		t.Warning = ui.Hex("#F0C38E")
		t.Success = ui.Hex("#A2D5AF")
		t.Scrollbar = ui.Hex("#B0BFB3")
		return workbenchTheme{
			Theme: t,
			Rail:  ui.Hex("#171E19"), Reading: ui.Hex("#1D2520"), Panel: ui.Hex("#202A23"),
			Well: ui.Hex("#29362D"), BubbleUser: ui.Hex("#324A3B"), BubbleContact: ui.Hex("#28332B"),
			WorkSurface: ui.Hex("#202A23"), WorkText: ui.Hex("#EDF3EE"),
			WorkMuted: ui.Hex("#B0BFB3"), WorkAttention: ui.Hex("#F0C38E"),
			WorkWarningBg: ui.Hex("#403222"),
		}
	}
	t.Background = ui.Hex("#FFFFFF")
	t.Surface = ui.Hex("#EEF2EF")
	t.SurfaceHover = ui.Hex("#E5EFE8")
	t.SurfacePressed = ui.Hex("#E5EAE6")
	t.Border = ui.Hex("#7C8A7F")
	t.Text = ui.Hex("#252E28")
	t.TextMuted = ui.Hex("#5D6B61")
	t.Accent = ui.Hex("#365E4D")
	t.AccentHover = ui.Hex("#2D5242")
	t.AccentPressed = ui.Hex("#244536")
	t.AccentText = ui.Hex("#FFFFFF")
	t.Selection = ui.Hex("#E5EFE8")
	t.Focus = ui.Hex("#3C7155")
	t.Danger = ui.Hex("#9B3C43")
	t.Warning = ui.Hex("#80501F")
	t.Success = ui.Hex("#31684A")
	t.Scrollbar = ui.Hex("#5D6B61")
	return workbenchTheme{
		Theme: t,
		Rail:  ui.Hex("#F5F7F5"), Reading: ui.Hex("#FFFFFF"), Panel: ui.Hex("#F8FAF8"),
		Well: ui.Hex("#EEF2EF"), BubbleUser: ui.Hex("#E5EFE8"), BubbleContact: ui.Hex("#F2F5F2"),
		WorkSurface: ui.Hex("#F8FAF8"), WorkText: ui.Hex("#252E28"),
		WorkMuted: ui.Hex("#5D6B61"), WorkAttention: ui.Hex("#80501F"),
		WorkWarningBg: ui.Hex("#FFF2DF"),
	}
}

// secondaryButton is the visible secondary-action face; PrimaryButton stays
// reserved for the main action. Do not set Background on this control: MyGo's
// styleButton only supplies hover/pressed while its default face is intact.
func secondaryButton(c *ui.Context, label string) ui.Element {
	return ui.Button(c, label).FontSize(13).FontWeight(500).MinHeight(32).Padding(6, 10)
}
