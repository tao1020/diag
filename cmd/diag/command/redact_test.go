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

package command

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestRedactionFlagsAndInvalidSelection(t *testing.T) {
	for _, makeCmd := range []func() *cobra.Command{newCollectCmd, newCollectkCmd, newCollectDMCmd, newRedactCmd} {
		cmd := makeCmd()
		require.NotNil(t, cmd.Flags().Lookup("redact-ips"))
		require.NotNil(t, cmd.Flags().Lookup("redact-all-ips"))
		require.NotNil(t, cmd.Flags().Lookup("redact-output"))
		require.NoError(t, cmd.Flags().Set("redact-ips", ""))
		require.Error(t, cmd.PreRunE(cmd, nil))
		require.NoError(t, cmd.Flags().Set("redact-ips", "host.example"))
		require.Error(t, cmd.PreRunE(cmd, nil))
	}
}

func TestAutomaticRedactCommand(t *testing.T) {
	root := t.TempDir()
	input, output := filepath.Join(root, "input"), filepath.Join(root, "output")
	require.NoError(t, os.Mkdir(input, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(input, "slow.log"), []byte("peer=10.1.1.1:4000 and 10.1.1.2"), 0600))
	cmd := newRedactCmd()
	cmd.SetArgs([]string{input, "--redact-all-ips", "--redact-output", output})
	require.NoError(t, cmd.Execute())
	data, err := os.ReadFile(filepath.Join(output, "slow.log"))
	require.NoError(t, err)
	require.Equal(t, "peer=198.18.0.1:4000 and 198.18.0.2", string(data))
	for _, makeCmd := range []func() *cobra.Command{newCollectCmd, newCollectkCmd, newCollectDMCmd, newRedactCmd} {
		cmd := makeCmd()
		require.NoError(t, cmd.Flags().Set("redact-all-ips", "true"))
		require.NoError(t, cmd.PreRunE(cmd, nil))
		require.NoError(t, cmd.Flags().Set("redact-ips", "10.1.1.1"))
		require.Error(t, cmd.PreRunE(cmd, nil))
	}
}

func TestQuestionMarkCLI(t *testing.T) {
	root := t.TempDir()
	input, output := filepath.Join(root, "input"), filepath.Join(root, "output")
	require.NoError(t, os.Mkdir(input, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(input, "slow.log"), []byte("host=10.1.1.1:4000"), 0600))
	cmd := newRedactCmd()
	cmd.SetArgs([]string{input, "--redact-all-ips", "--hide-log-ipv4", "--redact-output", output})
	require.NoError(t, cmd.Execute())
	body, err := os.ReadFile(filepath.Join(output, "slow.log"))
	require.NoError(t, err)
	require.Equal(t, "host=?:4000", string(body))
	for _, makeCmd := range []func() *cobra.Command{newCollectCmd, newCollectkCmd, newCollectDMCmd, newRedactCmd} {
		cmd := makeCmd()
		require.NoError(t, cmd.Flags().Set("hide-log-ipv4", "true"))
		require.ErrorContains(t, cmd.PreRunE(cmd, nil), "requires redact-all-ips")
		require.NoError(t, cmd.Flags().Set("redact-all-ips", "true"))
		require.NoError(t, cmd.PreRunE(cmd, nil))
	}
}

func TestInPlacePartialCLIWithPrivateMapping(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "input")
	require.NoError(t, os.Mkdir(input, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(input, "tidb.log"), []byte("peer=10.12.34.56:4000"), 0600))
	mapFile := filepath.Join(root, "ip-map.json")
	cmd := newRedactCmd()
	cmd.SetArgs([]string{input, "--redact-all-ips", "--keep-ipv4-octets", "1", "--redact-in-place", "--ip-map-output", mapFile})
	require.NoError(t, cmd.Execute())
	data, err := os.ReadFile(filepath.Join(input, "tidb.log"))
	require.NoError(t, err)
	require.Equal(t, "peer=10.0.0.56:4000", string(data))
	data, err = os.ReadFile(mapFile)
	require.NoError(t, err)
	require.Contains(t, string(data), `"original_ip": "10.12.34.56"`)
	_, err = os.Stat(input + "-redacted")
	require.True(t, os.IsNotExist(err))
}

func TestInPlaceAndPartialCLIValidation(t *testing.T) {
	for _, makeCmd := range []func() *cobra.Command{newCollectCmd, newCollectkCmd, newCollectDMCmd, newRedactCmd} {
		cmd := makeCmd()
		require.NoError(t, cmd.Flags().Set("redact-in-place", "true"))
		require.Error(t, cmd.PreRunE(cmd, nil))
		require.NoError(t, cmd.Flags().Set("redact-all-ips", "true"))
		require.NoError(t, cmd.PreRunE(cmd, nil))
		require.NoError(t, cmd.Flags().Set("redact-output", "other"))
		require.Error(t, cmd.PreRunE(cmd, nil))
		require.NoError(t, cmd.Flags().Set("redact-output", ""))
		require.NoError(t, cmd.Flags().Set("keep-ipv4-octets", "3"))
		require.Error(t, cmd.PreRunE(cmd, nil))
		require.NoError(t, cmd.Flags().Set("keep-ipv4-octets", "2"))
		require.NoError(t, cmd.Flags().Set("hide-log-ipv4", "true"))
		require.Error(t, cmd.PreRunE(cmd, nil))
	}
}

func TestMappingFailureDoesNotOverwriteCollectedData(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "tidb.log")
	require.NoError(t, os.WriteFile(file, []byte("peer=10.12.34.56"), 0600))
	cmd := newRedactCmd()
	cmd.SetArgs([]string{root, "--redact-all-ips", "--redact-in-place", "--ip-map-output", filepath.Join(root, "map.json")})
	require.ErrorContains(t, cmd.Execute(), "outside")
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Equal(t, "peer=10.12.34.56", string(data))
}

func TestRedactCommandAndFailedCollection(t *testing.T) {
	root := t.TempDir()
	input, output := filepath.Join(root, "input"), filepath.Join(root, "output")
	require.NoError(t, os.Mkdir(input, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(input, "cluster.json"), []byte(`{"host":"10.1.1.1"}`), 0600))
	cmd := newRedactCmd()
	parent := &cobra.Command{
		Use:                "diag",
		PersistentPreRunE:  func(_ *cobra.Command, _ []string) error { return errors.New("must not initialize TiUP for redaction") },
		PersistentPostRunE: func(_ *cobra.Command, _ []string) error { return errors.New("must not close TiUP for redaction") },
	}
	parent.AddCommand(cmd)
	parent.SetArgs([]string{"redact", input, "--redact-ips", "10.1.1.1", "--redact-output", output})
	require.NoError(t, parent.Execute())
	data, err := os.ReadFile(filepath.Join(output, "cluster.json"))
	require.NoError(t, err)
	require.JSONEq(t, `{"host":"198.18.0.1"}`, string(data))
	options := redactOptions{addresses: []string{"10.1.1.1"}}
	collectErr := errors.New("collection interrupted")
	require.ErrorIs(t, options.afterCollect(input, collectErr), collectErr)
	_, err = os.Stat(input + "-redacted")
	require.True(t, os.IsNotExist(err))
}
