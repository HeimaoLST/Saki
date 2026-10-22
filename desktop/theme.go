package main

import "github.com/egoist/mygo/ui"

// paperMossTheme maps the approved Paper & Moss reference onto the existing
// semantic widget theme: Paper in light appearance and Night Forest in dark.
// It is one theme mapping, not a new appearance/theme framework; #96 still
// owns mode selection and persistence.
func paperMossTheme(dark bool) *ui.Theme {
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
		t.Surface = ui.Hex("#24322b")
		t.SurfaceHover = ui.Hex("#2e3f35")
		t.SurfacePressed = ui.Hex("#354e3b")
		t.Border = ui.Hex("#405346")
		t.Text = ui.Hex("#e9ebdc")
		t.TextMuted = ui.Hex("#aab5a5")
		t.Accent = ui.Hex("#c7d7ac")
		t.AccentHover = ui.Hex("#d7e5c0")
		t.AccentPressed = ui.Hex("#aebf93")
		t.AccentText = ui.Hex("#1d2823")
		t.Selection = ui.Hex("#354e3b")
		t.Focus = ui.Hex("#dfae86")
		t.Danger = ui.Hex("#dfae86")
		t.Warning = ui.Hex("#dfae86")
		t.Success = ui.Hex("#c7d7ac")
		return t
	}
	t.Background = ui.Hex("#fbfaf6")
	t.Surface = ui.Hex("#fffdf8")
	t.SurfaceHover = ui.Hex("#f1eee4")
	t.SurfacePressed = ui.Hex("#e8e4d8")
	t.Border = ui.Hex("#ded8c9")
	t.Text = ui.Hex("#263c34")
	t.TextMuted = ui.Hex("#606b62")
	t.Accent = ui.Hex("#31594a")
	t.AccentHover = ui.Hex("#3c6a58")
	t.AccentPressed = ui.Hex("#26483c")
	t.AccentText = ui.Hex("#fbfaf6")
	t.Selection = ui.Hex("#e8efe6")
	t.Focus = ui.Hex("#985034")
	t.Danger = ui.Hex("#985034")
	t.Warning = ui.Hex("#985034")
	t.Success = ui.Hex("#31594a")
	return t
}
