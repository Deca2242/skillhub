package statefile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"skillhub/internal/shared/domain"
)

// This file freezes the reader of a version 1 binary: stateFile and
// decodeStateFile exactly as the base of the version 2 work had them
// (feat/v2 at ea62d69). A version 1 binary reads state.json first in every
// write (Save, RecordOperation, ClearOperation), so what this reader rejects
// is what an old binary can never overwrite. Never update it to follow the
// adapter: it stands for binaries that are already installed.

var errV1UnsupportedStateVersion = errors.New("unsupported state version")

type v1StateFile struct {
	Version    *int                           `json:"version,omitempty"`
	Candidates []domain.UpdateCandidate       `json:"candidates"`
	Skips      []domain.Skip                  `json:"skips"`
	Checks     domain.CheckTimes              `json:"checks"`
	Notified   domain.NotifiedMarks           `json:"notified"`
	Results    map[string]domain.TargetResult `json:"results,omitempty"`
	Operation  *domain.Operation              `json:"operation,omitempty"`
}

func decodeV1StateFile(data []byte) (v1StateFile, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var sf v1StateFile
	if err := dec.Decode(&sf); err != nil {
		return v1StateFile{}, err
	}
	var trailing struct{}
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return v1StateFile{}, errors.New("unexpected trailing data")
	}
	if sf.Version != nil && *sf.Version != 1 {
		return v1StateFile{}, fmt.Errorf("%w: %d", errV1UnsupportedStateVersion, *sf.Version)
	}
	return sf, nil
}
