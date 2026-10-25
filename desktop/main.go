// Saki desktop: a MyGo native-UI shell that talks to the Saki runtime over
// its stdio v1 JSONL boundary. The local fixture remains only for tests and
// screenshots; the shipped app uses the real runtime.
package main

import (
	_ "embed"
	"fmt"
	"log"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// The default Saki mascot is decoration shared by every contact. It is the
// same 256px PNG packaged for the browser prototype, embedded so the
// native preview has no file dependency at run time.
//
//go:embed assets/saki-avatar-default.png
var avatarPNG []byte

var avatarBMP *ui.Bitmap

func main() {
	a, err := newRuntimeApp()
	if err != nil {
		a = newUnavailableApp(err)
	}
	if err := loadAvatar(); err != nil {
		log.Printf("default avatar unavailable: %v", err)
	}
	a.requestQuit = mygo.App.Quit
	mygo.App.OnBeforeQuit(func(e *mygo.QuitEvent) {
		if a.quitReady {
			return
		}
		e.PreventDefault()
		a.beginShutdown()
	})
	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:         "Saki",
			TitleBarStyle: mygo.TitleBarHiddenInset,
			Width:         1040,
			Height:        720,
			MinWidth:      960,
			MinHeight:     640,
			Content:       ui.View(a.view),
		})
		a.win = win
		win.OnClose(func(e *mygo.CloseEvent) {
			if a.quitReady {
				return
			}
			e.PreventDefault()
			a.beginShutdown()
		})
		a.startRuntime()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

func loadAvatar() error {
	bmp, err := ui.DecodeBitmap(avatarPNG)
	if err != nil {
		return fmt.Errorf("decode default avatar: %w", err)
	}
	avatarBMP = bmp
	return nil
}

// newApp is the local fixture used by tests and preview screenshots. The
// shipped main path uses newRuntimeApp.
func newApp() (*appModel, error) {
	if err := loadAvatar(); err != nil {
		return nil, err
	}
	a := seededApp()
	a.demo = true
	return a, nil
}
