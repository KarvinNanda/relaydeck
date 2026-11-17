package server

import (
	"context"
	"errors"

	"github.com/ncruces/zenity"
)

// ErrPickCancelled means the user closed the folder dialog without a choice.
var ErrPickCancelled = errors.New("folder dialog cancelled")

// FolderPicker opens a native "choose folder" dialog on this machine. It is
// an interface so tests never open a real dialog.
type FolderPicker interface {
	PickFolder(ctx context.Context) (string, error)
}

// ZenityPicker is the real dialog (macOS, Windows, and Linux with zenity or
// a compatible tool installed). The dialog closes when ctx ends, for
// example when the browser drops the request.
type ZenityPicker struct{}

func (ZenityPicker) PickFolder(ctx context.Context) (string, error) {
	p, err := zenity.SelectFile(zenity.Directory(), zenity.Title("Choose a folder for relaydeck"), zenity.Context(ctx))
	if errors.Is(err, zenity.ErrCanceled) {
		return "", ErrPickCancelled
	}
	return p, err
}
