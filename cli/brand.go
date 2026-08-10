package cli

import (
	"os"

	"golang.org/x/term"
)

var colorOn = os.Getenv("NO_COLOR") == "" && term.IsTerminal(int(os.Stdout.Fd()))

const (
	cReset  = "\033[0m"
	cBold   = "\033[1m"
	cDim    = "\033[2m"
	cGreen  = "\033[38;2;58;208;127m"  // success / CTA green
	cBlue   = "\033[38;2;120;140;250m" // step accent
	cCyan   = "\033[38;2;34;211;238m"
	cPurple = "\033[38;2;168;85;247m" // brand bullet
)

func paint(color, s string) string {
	if !colorOn {
		return s
	}
	return color + s + cReset
}

func green(s string) string  { return paint(cGreen, s) }
func blue(s string) string   { return paint(cBlue, s) }
func cyan(s string) string   { return paint(cCyan, s) }
func purple(s string) string { return paint(cPurple, s) }
func dim(s string) string    { return paint(cDim, s) }
func bold(s string) string   { return paint(cBold, s) }
