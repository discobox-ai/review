// Command difftui is a terminal reimplementation of git-gui, the Tcl/Tk
// commit tool that ships with git. It keeps the same four pane layout:
// unstaged and staged file lists on the left, the diff and the commit message
// buffer on the right.
package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/discobox-ai/difftui/internal/git"
	"github.com/discobox-ai/difftui/internal/ui"
)

var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: difftui [-version] [path]\n\n"+
			"A console git-gui. With no path, the repository containing the\n"+
			"current directory is used.\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *showVersion {
		fmt.Println("difftui", version)
		return
	}

	dir := "."
	if flag.NArg() > 0 {
		dir = flag.Arg(0)
	}

	repo, err := git.Open(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "difftui:", err)
		os.Exit(1)
	}

	p := tea.NewProgram(ui.New(repo), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "difftui:", err)
		os.Exit(1)
	}
}
