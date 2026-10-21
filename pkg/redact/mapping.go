// Copyright 2026 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package redact

import (
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type mappingEntry struct {
	Original string `json:"original_ip"`
	Redacted string `json:"redacted_ip"`
}

// SaveMapping explicitly exports a private, local-only correspondence table.
// It must be outside input and output trees, and cannot overwrite any file.
// Call it before destructive rewriting so failure cannot lose the mapping.
func (r *Redactor) SaveMapping(filename, input, output string) error {
	input, err := filepath.Abs(input)
	if err != nil {
		return err
	}
	if output == "" {
		base, err := r.name(filepath.Base(input))
		if err != nil {
			return err
		}
		output = filepath.Join(filepath.Dir(input), base+"-redacted")
	}
	filename, err = resolvedLocation(filename)
	if err != nil {
		return err
	}
	for _, root := range []string{input, output} {
		root, err = resolvedLocation(root)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("ip-map-output must be outside both original and redacted data directories")
		}
	}
	if _, err := os.Lstat(filename); !os.IsNotExist(err) {
		return errors.New("ip-map-output exists or cannot be checked; refusing to overwrite")
	}
	keys := make([]netip.Addr, 0, len(r.mapping))
	for addr := range r.mapping {
		keys = append(keys, addr)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Less(keys[j]) })
	entries := make([]mappingEntry, 0, len(keys))
	for _, addr := range keys {
		entries = append(entries, mappingEntry{Original: addr.String(), Redacted: r.mapping[addr].String()})
	}
	payload := struct {
		Version        int            `json:"version"`
		Notice         string         `json:"notice"`
		LogIPv4Hidden  bool           `json:"log_ipv4_hidden"`
		KeptIPv4Octets int            `json:"kept_ipv4_octets"`
		Entries        []mappingEntry `json:"entries"`
	}{1, "Private local IP map. Never include in a diagnostic package. Use only after redaction completes successfully. Question-mark log body addresses cannot be recovered.", r.logIPv4QuestionMark, r.keptIPv4Octets, entries}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(filename), ".diag-ip-map-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	// Publish complete data without replacing an existing path or following
	// a destination symlink. Both names are on the same filesystem.
	return os.Link(file.Name(), filename)
}

func resolvedLocation(name string) (string, error) {
	abs, err := filepath.Abs(name)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}
