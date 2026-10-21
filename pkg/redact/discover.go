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
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
)

// Discover scans supported collected files and paths for literal IPs without
// saving an IP inventory. It uses the same container readers as copying and
// verification, but does not recompress inspected data.
func Discover(input string) (*Redactor, error) {
	input, err := filepath.Abs(input)
	if err != nil {
		return nil, err
	}
	root, err := os.Lstat(input)
	if err != nil || !root.IsDir() {
		return nil, errors.New("IP discovery requires a real collected directory")
	}
	if _, err := os.Lstat(filepath.Join(input, ".collect.lock")); !os.IsNotExist(err) {
		return nil, errors.New("collection is incomplete or still running; refusing IP discovery")
	}
	if err := CheckInPlaceComplete(input); err != nil {
		return nil, err
	}
	scan := &Redactor{discovered: make(map[netip.Addr]bool), scanOnly: true, remaining: maxExpandedBytes}
	_, err = scan.name(filepath.Base(input))
	if err != nil {
		return nil, err
	}
	err = filepath.WalkDir(input, func(file string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := scan.entry(); err != nil {
			return err
		}
		rel, err := filepath.Rel(input, file)
		if err != nil {
			return err
		}
		if _, err := scan.name(filepath.ToSlash(rel)); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("symlinks and special files cannot be safely inspected")
		}
		in, err := os.Open(file)
		if err != nil {
			return err
		}
		defer in.Close()
		return scan.transform(in, io.Discard, rel, 0)
	})
	if err != nil {
		return nil, err
	}
	addresses := make([]string, 0, len(scan.discovered))
	for addr := range scan.discovered {
		addresses = append(addresses, addr.String())
	}
	var redactor *Redactor
	if len(addresses) == 0 {
		redactor = &Redactor{mapping: make(map[netip.Addr]netip.Addr), destinations: make(map[netip.Addr]bool)}
	} else {
		redactor, err = New(addresses)
		if err != nil {
			return nil, err
		}
	}
	redactor.automatic = true
	return redactor, nil
}
