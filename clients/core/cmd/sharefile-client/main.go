// sharefile-client is a private JSON-lines bridge, not a shell credential interface.
// Send one request on stdin. stdout contains progress events and exactly one final event.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sharefile/desktop/client"
	"syscall"
)

type request struct {
	Operation string              `json:"operation"`
	Origin    string              `json:"origin"`
	Token     string              `json:"token"`
	Name      string              `json:"name"`
	RequestID string              `json:"request_id"`
	Secret    string              `json:"secret"`
	ShareID   string              `json:"share_id"`
	Parent    string              `json:"parent"`
	Source    string              `json:"source"`
	Job       client.Job          `json:"job"`
	Directory client.DirectoryJob `json:"directory_job"`
	Options   client.ShareOptions `json:"options"`
}
type event struct {
	Type   string `json:"type"`
	Result any    `json:"result,omitempty"`
	Job    any    `json:"job,omitempty"`
	Error  string `json:"error,omitempty"`
}

func run(ctx context.Context, in request, emit func(event)) (any, error) {
	if in.Operation == "normalize" {
		return client.NormalizeOrigin(in.Origin)
	}
	if in.Operation == "prepare" {
		return client.Prepare(in.Source, in.Parent)
	}
	if in.Operation == "prepare_directory" {
		return client.PrepareDirectory(in.Source, in.Parent)
	}
	c, err := client.New(in.Origin, in.Token)
	if err != nil {
		return nil, err
	}
	switch in.Operation {
	case "session":
		return c.Me(ctx)
	case "folders":
		return c.Folders(ctx, in.Parent)
	case "folder":
		return c.Folder(ctx, in.Parent)
	case "pair_start":
		return c.StartPairing(ctx, in.Name)
	case "pair_poll":
		token, err := c.PollPairing(ctx, in.RequestID, in.Secret)
		return map[string]string{"token": token}, err
	case "upload":
		err := c.Upload(ctx, &in.Job, func(j client.Job) { emit(event{Type: "progress", Job: &j}) })
		return in.Job, err
	case "upload_directory":
		err := c.UploadDirectory(ctx, &in.Directory, func(j client.DirectoryJob) { emit(event{Type: "progress", Job: j}) })
		return in.Directory, err
	case "cancel_directory":
		return nil, c.CancelDirectory(ctx, &in.Directory)
	case "cancel":
		return nil, c.Cancel(ctx, &in.Job)
	case "share":
		return c.Share(ctx, in.Job.FileID, in.Options)
	case "revoke_share":
		return nil, c.RevokeShare(ctx, in.ShareID)
	default:
		return nil, fmt.Errorf("Неизвестная операция")
	}
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	enc := json.NewEncoder(os.Stdout)
	emit := func(e event) {
		if enc.Encode(e) != nil {
			stop()
			os.Exit(1)
		}
	}
	var in request
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 16<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil {
		emit(event{Type: "error", Error: "Некорректный запрос клиента"})
		return
	}
	out, err := run(ctx, in, emit)
	if err != nil {
		emit(event{Type: "error", Error: err.Error()})
		return
	}
	emit(event{Type: "result", Result: out})
}
