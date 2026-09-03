// Command difftui reviews a branch in a terminal.
//
// With no subcommand it opens the window. With one it reads or writes the same
// review from a script — see the cli package.
//
// The window is a contents page and a history on the left, and on the right
// every change the branch makes, from its merge base to the working tree, in
// one scroll — with the comments written into it where they were made. Files
// are approved, lines are commented on, and the whole verdict is kept in a
// JSON file in the git directory that something else can read and write.
//
// It reads a repository and never writes one: nothing here stages, commits or
// rewrites anything.
package main

import (
	"flag"
	"fmt"
	"os"

	"golang.org/x/term"

	tea "charm.land/bubbletea/v2"
	"github.com/discobox-ai/difftui/internal/cli"
	"github.com/discobox-ai/difftui/internal/git"
	"github.com/discobox-ai/difftui/internal/ui"
)

var version = "dev"

func main() {
	// A subcommand is read before the flag package sees anything, so that a
	// remark can be written without quoting rules getting in the way of it.
	if len(os.Args) > 1 && cli.Is(os.Args[1]) {
		repo, err := git.Open(".")
		if err != nil {
			fmt.Fprintln(os.Stderr, "difftui:", err)
			os.Exit(1)
		}
		if err := cli.Run(repo, os.Args[1:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "difftui:", err)
			os.Exit(1)
		}
		return
	}

	showVersion := flag.Bool("version", false, "print the version and exit")
	base := flag.String("base", "", "review from this ref instead of the one difftui works out")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: difftui [-version] [-base <ref>] [path]\n"+
			"       difftui <command> [args]\n\n"+
			"Review a branch: every change from its merge base to the working\n"+
			"tree, approved file by file and commented on line by line. With no\n"+
			"path, the repository containing the current directory is used.\n\n")
		flag.PrintDefaults()
		fmt.Fprintln(os.Stderr)
		cli.Usage(os.Stderr)
	}
	flag.Parse()

	if *showVersion {
		fmt.Println("difftui", version)
		return
	}

	// With nowhere to draw, opening the window would put a screenful of escape
	// sequences down a pipe and then wait for keys that are never coming. What
	// is nearly always meant by running difftui that way is "tell me how to
	// use this from a script", so that is what it does.
	//
	// The note goes to stderr and the usage to stdout, so that whatever asked
	// gets the usable half on its own.
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Fprintln(os.Stderr, "difftui: no terminal to draw in; these work without one.")
		cli.Usage(os.Stdout)
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

	// The alternate screen, the mouse and the keyboard enhancements are part
	// of what the model's view asks the screen to be, rather than options
	// settled here — see ui.Model.View.
	p := tea.NewProgram(ui.New(repo, *base))
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "difftui:", err)
		os.Exit(1)
	}
}
