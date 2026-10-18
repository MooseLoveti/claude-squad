package log

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInitializeWithoutAWritableLogFile(t *testing.T) {
	saved := logFileName
	t.Cleanup(func() { logFileName, globalLogFile = saved, nil })
	logFileName = filepath.Join(t.TempDir(), "missing", "claudesquad.log")

	require.NotPanics(t, func() { Initialize("[test] ") }, "a read-only sandbox must not stop the command")
	require.Nil(t, globalLogFile)
	require.NotNil(t, ErrorLog)
	require.NotPanics(t, Close)
}
