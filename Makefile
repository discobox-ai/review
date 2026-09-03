BINARY := discobox-review

.PHONY: build install test vet fmt clean screenshots

build:
	go build -o $(BINARY) .

install:
	go install .

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

clean:
	rm -f $(BINARY)

# ---------------------------------------------------------------------------
# The README's pictures
# ---------------------------------------------------------------------------
#
# They are taken from the running window rather than drawn by hand: the
# generator in internal/ui drives the real model over a fixture repository and
# writes out the frames, and freeze turns each frame into a picture. A picture
# of a program is a claim about it, and this is how the claim stays true.
#
#   go install github.com/charmbracelet/freeze@latest
#   apt install imagemagick fonts-jetbrains-mono
#
# The font has to hold everything the window draws. JetBrains Mono does; Hack,
# for one, has no check mark in it at all.
SHOTDIR   := docs/screenshots
SHOTFONT  ?= JetBrains Mono
SHOTWIDTH ?= 1400

screenshots:
	@command -v freeze >/dev/null || \
		{ echo "need freeze: go install github.com/charmbracelet/freeze@latest"; exit 1; }
	@command -v convert >/dev/null || { echo "need ImageMagick"; exit 1; }
	@command -v fc-match >/dev/null || { echo "need fontconfig"; exit 1; }
	@mkdir -p $(SHOTDIR)
	SHOTS=$(CURDIR)/$(SHOTDIR) go test ./internal/ui -run TestWriteScreenshots -count=1
	@font=$$(fc-match -f '%{file}' "$(SHOTFONT)"); \
	for f in $(SHOTDIR)/*.ansi; do \
		png=$${f%.ansi}.png; \
		freeze "$$f" -o "$$png" --font.file "$$font" \
			--window --border.radius 8 --padding 16 --margin 20 \
			--shadow.blur 20 --shadow.x 0 --shadow.y 10 >/dev/null; \
		convert "$$png" -resize $(SHOTWIDTH)x -strip -colors 256 "$$png"; \
		echo "  $$png"; \
	done
	@rm -f $(SHOTDIR)/*.ansi
