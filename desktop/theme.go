package main

import "github.com/egoist/mygo/ui"

// roseTheme maps the accepted Rose Pine (dark) / Rosé Pine Dawn (light)
// palette onto the native widget theme. It starts from the framework theme
// for the detected OS appearance so unset roles stay coherent.
func roseTheme(dark bool) *ui.Theme {
	t := ui.LightTheme()
	t.FontSize = 14
	t.Spacing = 4
	t.Radius = 10
	if dark {
		t = ui.DarkTheme()
		t.FontSize = 14
		t.Spacing = 4
		t.Radius = 10
		t.Background = ui.Hex("#191724")
		t.Surface = ui.Hex("#1f1d2e")
		t.SurfaceHover = ui.Hex("#26233a")
		t.SurfacePressed = ui.Hex("#26233a")
		t.Border = ui.Hex("#403d52")
		t.Text = ui.Hex("#e0def4")
		t.TextMuted = ui.Hex("#908caa")
		t.Accent = ui.Hex("#ebbcba")
		t.AccentHover = ui.Hex("#f0d0ce")
		t.AccentPressed = ui.Hex("#d9a9a7")
		t.AccentText = ui.Hex("#191724")
		t.Selection = ui.Hex("#26233a")
		t.Focus = ui.Hex("#c4a7e7")
		t.Danger = ui.Hex("#eb6f92")
		t.Warning = ui.Hex("#f6c177")
		t.Success = ui.Hex("#9ccfd8")
		return t
	}
	t.Background = ui.Hex("#faf4ed")
	t.Surface = ui.Hex("#fffaf3")
	t.SurfaceHover = ui.Hex("#f2e9e1")
	t.SurfacePressed = ui.Hex("#f2e9e1")
	t.Border = ui.Hex("#dfdad9")
	t.Text = ui.Hex("#575279")
	t.TextMuted = ui.Hex("#6e6a86")
	t.Accent = ui.Hex("#d7827e")
	t.AccentHover = ui.Hex("#e09a96")
	t.AccentPressed = ui.Hex("#c56f6b")
	t.AccentText = ui.Hex("#191724")
	t.Selection = ui.Hex("#f2e9e1")
	t.Focus = ui.Hex("#907aa9")
	t.Danger = ui.Hex("#b4637a")
	t.Warning = ui.Hex("#ea9d34")
	t.Success = ui.Hex("#286983") // Dawn pine: readable links and Working
	return t
}
