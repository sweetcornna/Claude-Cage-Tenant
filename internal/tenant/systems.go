package tenant

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const defaultSystemID = "debian-12"

var (
	systemIDPattern = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	imagePattern    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
)

type systemOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type System struct {
	systemOption
	Image string `json:"image"`
}

func DefaultSystem(image string) System {
	return System{systemOption: systemOption{ID: defaultSystemID, Label: "Debian 12"}, Image: image}
}

func LoadSystems(filename string) ([]System, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	const limit = 64 << 10
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("systems file must be a regular JSON file under 64 KiB")
	}
	decoder := json.NewDecoder(io.LimitReader(file, limit+1))
	decoder.DisallowUnknownFields()
	var systems []System
	if decoder.Decode(&systems) != nil || systems == nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("systems file must contain a JSON array of id, label and image entries")
	}
	_, err = systemIndex(systems)
	return systems, err
}

func validSystemLabel(label string) bool {
	return label != "" && strings.TrimSpace(label) == label && utf8.RuneCountInString(label) <= 64 && strings.IndexFunc(label, unicode.IsControl) == -1
}

func systemIndex(systems []System) (map[string]System, error) {
	index := make(map[string]System, len(systems))
	if len(systems) > 128 {
		return nil, errors.New("too many configured systems")
	}
	for _, system := range systems {
		if !secretPattern.MatchString(system.Image) {
			return nil, errors.New("system images must be full local Incus fingerprints")
		}
		if !systemIDPattern.MatchString(system.ID) || !validSystemLabel(system.Label) || index[system.ID].ID != "" {
			return nil, errors.New("invalid or duplicate system configuration")
		}
		index[system.ID] = system
	}
	return index, nil
}

func (s *Server) listSystems(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	session := s.expectedSessionLocked(w, r)
	s.mu.Unlock()
	if session == nil {
		return
	}
	options := make([]systemOption, 0, len(s.cfg.Systems))
	for _, system := range s.cfg.Systems {
		options = append(options, system.systemOption)
	}
	jsonReply(w, http.StatusOK, map[string][]systemOption{"systems": options})
}
