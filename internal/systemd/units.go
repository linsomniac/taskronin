package systemd

import (
	"bytes"
	"embed"
	"fmt"
	"strings"
	"text/template"
	"unicode"
)

//go:embed units/*.tmpl
var unitFiles embed.FS

var unitTemplates = template.Must(template.ParseFS(unitFiles, "units/*.tmpl"))

// UnitData supplies the resolved paths for the systemd user unit templates.
type UnitData struct {
	Binary, Socket, DataDir, StateDir, RuntimeDir string
	ExtraRW                                       []string
}

// RenderUnits renders the architecture's user units. Callers install both
// files with mode 0644, including when their process umask is 0077.
func RenderUnits(d UnitData) (service, socket []byte, err error) {
	paths := []struct{ field, path string }{
		{"Binary", d.Binary}, {"Socket", d.Socket}, {"DataDir", d.DataDir},
		{"StateDir", d.StateDir}, {"RuntimeDir", d.RuntimeDir},
	}
	for _, path := range d.ExtraRW {
		paths = append(paths, struct{ field, path string }{"ExtraRW", path})
	}
	for _, p := range paths {
		// AIDEV-NOTE: the templates deliberately do no systemd escaping;
		// whitespace, specifiers, quotes and controls must never reach them.
		if strings.ContainsFunc(p.path, func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("%\"\\", r)
		}) {
			return nil, nil, fmt.Errorf("unsafe systemd %s path %q", p.field, p.path)
		}
	}
	var serviceBuf, socketBuf bytes.Buffer
	if err := unitTemplates.ExecuteTemplate(&serviceBuf, "taskronin.service.tmpl", d); err != nil {
		return nil, nil, fmt.Errorf("render systemd service: %w", err)
	}
	if err := unitTemplates.ExecuteTemplate(&socketBuf, "taskronin.socket.tmpl", d); err != nil {
		return nil, nil, fmt.Errorf("render systemd socket: %w", err)
	}
	return serviceBuf.Bytes(), socketBuf.Bytes(), nil
}
