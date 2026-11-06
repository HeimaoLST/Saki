// Saki desktop: a MyGo native-UI shell that talks to the Saki runtime over
// its stdio v1 JSONL boundary. The local fixture remains only for tests and
// screenshots; the shipped app uses the real runtime.
package main

import (
	_ "embed"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

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
	if len(os.Args) == 2 && os.Args[1] == "--issue-tool-helper" {
		if err := runIssueToolHelperCLI(os.Stdin, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "issue tool helper:", err)
			os.Exit(1)
		}
		return
	}
	windowOptions := mygo.WindowOptions{
		Title:     "Nagi",
		Width:     1040,
		Height:    720,
		MinWidth:  720,
		MinHeight: 640,
	}
	if size := os.Getenv("SAKI_GUI_CONTENT_SIZE"); size != "" {
		widthText, heightText, ok := strings.Cut(size, "x")
		width, widthErr := strconv.Atoi(widthText)
		height, heightErr := strconv.Atoi(heightText)
		if !ok || widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
			log.Fatal("SAKI_GUI_CONTENT_SIZE must be positive integer WIDTHxHEIGHT in DIPs, for example 960x640")
		}
		windowOptions.Width, windowOptions.Height = width, height
		windowOptions.UseContentSize = true
	}
	// The visible product name is Nagi; internal identifiers, the Saki.app
	// artifact and Saki data paths stay as they are. Call before Run so the
	// default app menu, About, Hide and Quit use Nagi too.
	mygo.App.SetName("Nagi")
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
		windowOptions.Content = ui.View(a.view)
		win := mygo.NewWindow(windowOptions)
		a.win = win
		updateContentSize := func(event string) {
			w, h := win.ContentSize()
			if w > 0 {
				a.windowWidth = w
			}
			if windowOptions.UseContentSize {
				log.Printf("nagi native-content-size event=%s unit=DIP requested_width=%d requested_height=%d actual_width=%d actual_height=%d",
					event, windowOptions.Width, windowOptions.Height, w, h)
			}
		}
		updateContentSize("created")
		win.OnResize(func() {
			updateContentSize("resize")
		})
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
