// Saki desktop native design preview: a MyGo native-UI shell for the
// accepted contact interface. This is a design prototype, not the native
// runtime; model replies, statuses and history are local demo data.
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
	a, err := newApp()
	if err != nil {
		log.Fatal(err)
	}
	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:     "Saki — Design preview",
			Width:     1040,
			Height:    720,
			MinWidth:  960,
			MinHeight: 640,
			Content:   ui.View(a.view),
		})
		a.win = win
		a.startSeeded()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

func newApp() (*appModel, error) {
	bmp, err := ui.DecodeBitmap(avatarPNG)
	if err != nil {
		return nil, fmt.Errorf("decode default avatar: %w", err)
	}
	avatarBMP = bmp
	return seededApp(), nil
}
