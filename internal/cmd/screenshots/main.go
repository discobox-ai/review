// Command screenshots takes the README's pictures.
//
// They are taken from the running window rather than drawn by hand: the
// generator in pkg/ui drives the real model over a fixture repository and
// writes out the frames, and freeze turns each frame into a picture. A picture
// of a program is a claim about it, and this is how the claim stays true.
//
// freeze, ImageMagick and the font come from the flake's screenshots shell:
//
//	nix develop .#screenshots -c go tool task screenshots
//
// The font has to hold everything the window draws. JetBrains Mono does; Hack,
// for one, has no check mark in it at all.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	dir := flag.String("dir", "docs/screenshots", "where the pictures go")
	font := flag.String("font", "JetBrains Mono", "the font to draw them in, as fontconfig names it")
	width := flag.Int("width", 1400, "the width to scale each picture to, in pixels")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, *dir, *font, *width); err != nil {
		stop()
		fmt.Fprintln(os.Stderr, "screenshots:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, dir, font string, width int) error {
	convert, err := imageMagick()
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("freeze"); err != nil {
		return errors.New("need freeze")
	}
	if _, err := exec.LookPath("fc-match"); err != nil {
		return errors.New("need fontconfig's fc-match")
	}
	// freeze takes only TrueType, and a font is often installed both ways.
	fontFile, err := output(ctx, "fc-match", "-f", "%{file}", font+":fontformat=TrueType")
	if err != nil {
		return fmt.Errorf("finding font %q: %w", font, err)
	}

	dir, err = filepath.Abs(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	test := exec.CommandContext(ctx, "go", "test", "./pkg/ui", "-run", "TestWriteScreenshots", "-count=1")
	test.Env = append(os.Environ(), "SHOTS="+dir)
	test.Stdout, test.Stderr = os.Stdout, os.Stderr
	if err := test.Run(); err != nil {
		return fmt.Errorf("writing the frames: %w", err)
	}

	frames, err := filepath.Glob(filepath.Join(dir, "*.ansi"))
	if err != nil {
		return err
	}
	for _, frame := range frames {
		png := strings.TrimSuffix(frame, ".ansi") + ".png"
		if _, err := output(ctx, "freeze", frame, "-o", png, "--font.file", fontFile,
			"--window", "--border.radius", "8", "--padding", "16", "--margin", "20",
			"--shadow.blur", "20", "--shadow.x", "0", "--shadow.y", "10"); err != nil {
			return fmt.Errorf("freeze %s: %w", filepath.Base(frame), err)
		}
		if _, err := output(ctx, convert, png, "-resize", strconv.Itoa(width)+"x", "-strip", "-colors", "256", png); err != nil {
			return fmt.Errorf("convert %s: %w", filepath.Base(png), err)
		}
		if err := os.Remove(frame); err != nil {
			return err
		}
		fmt.Println(" ", png)
	}
	return nil
}

// imageMagick returns ImageMagick's command: magick in version 7, convert
// before it.
func imageMagick() (string, error) {
	for _, name := range []string{"magick", "convert"} {
		if _, err := exec.LookPath(name); err == nil {
			return name, nil
		}
	}
	return "", errors.New("need ImageMagick")
}

// output runs a command and returns what it printed. When it fails, the error
// carries everything it printed, since freeze reports its errors on stdout.
func output(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if said := strings.TrimSpace(stderr.String() + "\n" + stdout.String()); said != "" {
			return "", fmt.Errorf("%w: %s", err, said)
		}
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}
