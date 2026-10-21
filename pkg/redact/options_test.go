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
	"compress/gzip"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeFixture(t *testing.T, root, name, body string) {
	t.Helper()
	file := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0700))
	require.NoError(t, os.WriteFile(file, []byte(body), 0600))
}

func TestRewriteMatchesCopyAndPreservesReferences(t *testing.T) {
	root := t.TempDir()
	input, work, copyDir := filepath.Join(root, "input"), filepath.Join(root, "work"), filepath.Join(root, "copy")
	writeFixture(t, input, "10.12.34.56/tidb.log", "peer=10.12.34.56:4000 other=10.12.35.57\n")
	writeFixture(t, input, "10.12.35.57/tidb.log", "peer=10.12.35.57:4000\n")
	writeFixture(t, input, "cluster.json", `{"hosts":["10.12.34.56","10.12.35.57"],"log_files":["10.12.34.56/tidb.log","10.12.35.57/tidb.log"]}`)
	f, err := os.Create(filepath.Join(input, "tidb.log.gz"))
	require.NoError(t, err)
	gw := gzip.NewWriter(f)
	_, err = gw.Write([]byte("peer=10.12.34.56\n"))
	require.NoError(t, err)
	require.NoError(t, gw.Close())
	require.NoError(t, f.Close())
	require.NoError(t, os.CopyFS(work, os.DirFS(input)))
	copyRedactor, err := Discover(input)
	require.NoError(t, err)
	require.NoError(t, copyRedactor.KeepIPv4Octets(2))
	_, _, err = copyRedactor.Copy(input, copyDir)
	require.NoError(t, err)
	r, err := Discover(work)
	require.NoError(t, err)
	require.NoError(t, r.KeepIPv4Octets(2))
	output, report, err := r.Rewrite(work)
	require.NoError(t, err)
	require.Equal(t, work, output)
	require.Equal(t, 4, report.Files)
	require.NoError(t, CheckInPlaceComplete(work))
	count := 0
	require.NoError(t, filepath.WalkDir(copyDir, func(file string, entry os.DirEntry, err error) error {
		require.NoError(t, err)
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(copyDir, file)
		require.NoError(t, err)
		expected, err := os.ReadFile(file)
		require.NoError(t, err)
		actual, err := os.ReadFile(filepath.Join(work, rel))
		require.NoError(t, err)
		require.Equal(t, expected, actual)
		count++
		return nil
	}))
	require.Equal(t, 4, count)
	_, err = os.Stat(filepath.Join(work, "10.12.34.56"))
	require.True(t, os.IsNotExist(err))
	data, err := os.ReadFile(filepath.Join(work, "cluster.json"))
	require.NoError(t, err)
	var metadata struct {
		Logs []string `json:"log_files"`
	}
	require.NoError(t, json.Unmarshal(data, &metadata))
	for _, name := range metadata.Logs {
		_, err := os.Stat(filepath.Join(work, name))
		require.NoError(t, err)
	}
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 3) // No second full rewrite directory was created.
}

func TestRewritePreflightLeavesOriginalUntouched(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "a.log", "peer=10.1.1.1\n")
	require.NoError(t, os.WriteFile(filepath.Join(root, "z.pb"), []byte{0, 1, 2}, 0600))
	r, err := New([]string{"10.1.1.1"})
	require.NoError(t, err)
	_, _, err = r.Rewrite(root)
	require.ErrorContains(t, err, "preflight")
	data, err := os.ReadFile(filepath.Join(root, "a.log"))
	require.NoError(t, err)
	require.Equal(t, "peer=10.1.1.1\n", string(data))
	require.NoError(t, CheckInPlaceComplete(root))
}

func TestInterruptedRewriteIsBlocked(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "input")
	writeFixture(t, input, "a.log", "peer=10.1.1.1\n")
	writeFixture(t, input, "b.log", "peer=10.1.1.2\n")
	r, err := Discover(input)
	require.NoError(t, err)
	require.NoError(t, r.MaskLogIPv4())
	plan, err := r.prepareRewrite(input)
	require.NoError(t, err)
	writeFixture(t, input, "b.log", "peer=10.1.1.2 changed after preflight\n")
	require.ErrorContains(t, r.applyRewrite(input, plan), "changed after preflight")
	data, err := os.ReadFile(filepath.Join(input, "a.log"))
	require.NoError(t, err)
	require.Equal(t, "peer=?\n", string(data))
	require.ErrorContains(t, CheckInPlaceComplete(input), "incomplete")
	_, err = Discover(input)
	require.Error(t, err)
	_, _, err = r.Copy(input, filepath.Join(root, "out"))
	require.Error(t, err)
	_, _, err = r.Rewrite(input)
	require.Error(t, err)
	files, err := os.ReadDir(input)
	require.NoError(t, err)
	for _, file := range files {
		require.False(t, strings.HasPrefix(file.Name(), ".diag-redact-file-"))
	}
}

func TestRewriteNestedNamesAndCollision(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "10.1.1.1/10.1.1.2/10.1.1.3.log", "peer=10.1.1.1\n")
	r, err := Discover(root)
	require.NoError(t, err)
	_, _, err = r.Rewrite(root)
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(root, "198.18.0.1/198.18.0.2/198.18.0.3.log"))
	require.NoError(t, err)
	require.Equal(t, "peer=198.18.0.1\n", string(data))
	other := t.TempDir()
	writeFixture(t, other, "2001:db8::1/tidb.log", "peer=[2001:db8::1]")
	writeFixture(t, other, "2001:db8:0:0:0:0:0:1/tidb.log", "peer=[2001:db8::1]")
	r, err = Discover(other)
	require.NoError(t, err)
	_, _, err = r.Rewrite(other)
	require.ErrorContains(t, err, "collision")
	require.NoError(t, CheckInPlaceComplete(other))
}

func TestKeepIPv4OctetsAndRejectAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		count    int
		expected string
	}{{1, "10.0.0.56"}, {2, "198.18.34.56"}} {
		r, err := New([]string{"10.12.34.56", "fd00::1"})
		require.NoError(t, err)
		require.NoError(t, r.KeepIPv4Octets(tc.count))
		data, err := r.text([]byte("peer=10.12.34.56:4000 ipv6=[fd00::1]"))
		require.NoError(t, err)
		require.Contains(t, string(data), "peer="+tc.expected+":4000")
	}
	r, err := New([]string{"10.12.34.56", "172.16.34.56"})
	require.NoError(t, err)
	before := r.mapping[netip.MustParseAddr("10.12.34.56")]
	require.ErrorContains(t, r.KeepIPv4Octets(2), "suffix collision")
	require.Equal(t, before, r.mapping[netip.MustParseAddr("10.12.34.56")])
	require.Error(t, r.KeepIPv4Octets(3))
	r, err = New([]string{"198.18.34.56"})
	require.NoError(t, err)
	require.NoError(t, r.KeepIPv4Octets(2))
	require.Equal(t, netip.MustParseAddr("198.19.34.56"), r.mapping[netip.MustParseAddr("198.18.34.56")])
}

func TestOneOctetFixedPrefixSafety(t *testing.T) {
	for _, addresses := range [][]string{
		{"172.16.34.56", "192.168.8.56"},
		{"10.0.0.56"},
		{"172.16.34.56", "10.0.0.56"},
	} {
		r, err := New(addresses)
		require.NoError(t, err)
		before := make(map[netip.Addr]netip.Addr, len(r.mapping))
		for original, replacement := range r.mapping {
			before[original] = replacement
		}
		require.Error(t, r.KeepIPv4Octets(1))
		require.Equal(t, before, r.mapping)
	}
	root := t.TempDir()
	writeFixture(t, root, "172.16.34.56/tidb.log", "peer=172.16.34.56:4000 other=192.168.8.57:20160\n")
	writeFixture(t, root, "cluster.json", `{"hosts":["172.16.34.56","192.168.8.57"],"log_files":["172.16.34.56/tidb.log"]}`)
	r, err := Discover(root)
	require.NoError(t, err)
	require.NoError(t, r.KeepIPv4Octets(1))
	_, _, err = r.Rewrite(root)
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(root, "10.0.0.56/tidb.log"))
	require.NoError(t, err)
	require.Equal(t, "peer=10.0.0.56:4000 other=10.0.0.57:20160\n", string(body))
	body, err = os.ReadFile(filepath.Join(root, "cluster.json"))
	require.NoError(t, err)
	require.JSONEq(t, `{"hosts":["10.0.0.56","10.0.0.57"],"log_files":["10.0.0.56/tidb.log"]}`, string(body))
}

func TestPrivateMappingLocationPermissionsAndContents(t *testing.T) {
	root := t.TempDir()
	input, output := filepath.Join(root, "input"), filepath.Join(root, "output")
	writeFixture(t, input, "tidb.log", "peer=10.12.34.56")
	r, err := Discover(input)
	require.NoError(t, err)
	require.NoError(t, r.KeepIPv4Octets(2))
	file := filepath.Join(root, "ip-map.json")
	require.NoError(t, r.SaveMapping(file, input, output))
	info, err := os.Stat(file)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	var payload struct {
		Entries []mappingEntry `json:"entries"`
		Kept    int            `json:"kept_ipv4_octets"`
	}
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, 2, payload.Kept)
	require.Equal(t, []mappingEntry{{Original: "10.12.34.56", Redacted: "198.18.34.56"}}, payload.Entries)
	require.Error(t, r.SaveMapping(file, input, output))
	require.Error(t, r.SaveMapping(filepath.Join(input, "map.json"), input, output))
	require.NoError(t, os.Mkdir(output, 0700))
	require.Error(t, r.SaveMapping(filepath.Join(output, "map.json"), input, output))
	require.NoError(t, os.Symlink(input, filepath.Join(root, "alias")))
	require.Error(t, r.SaveMapping(filepath.Join(root, "alias", "map.json"), input, output))
}
