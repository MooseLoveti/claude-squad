package log

import (
	"io"
	"log"
	"os"
	"path/filepath"
)

var (
	WarningLog *log.Logger
	InfoLog    *log.Logger
	ErrorLog   *log.Logger
)

var logFileName = filepath.Join(os.TempDir(), "claudesquad.log")

var globalLogFile *os.File

// Initialize should be called once at the beginning of the program to set up
// logging to a file in the os temp directory; defer Close() after calling it.
// tag prefixes every line, telling apart the processes sharing the file.
// Where the file cannot be written, as inside an agent's read-only sandbox,
// logs go to stderr instead.
func Initialize(tag string) {
	var out io.Writer = os.Stderr
	if f, err := os.OpenFile(logFileName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666); err == nil {
		out, globalLogFile = f, f
	}

	// Set log format to include timestamp and file/line number
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	InfoLog = log.New(out, tag+"INFO:", log.Ldate|log.Ltime|log.Lshortfile)
	WarningLog = log.New(out, tag+"WARNING:", log.Ldate|log.Ltime|log.Lshortfile)
	ErrorLog = log.New(out, tag+"ERROR:", log.Ldate|log.Ltime|log.Lshortfile)
}

// Close closes the log file. It prints nothing: the hook and status line
// commands write to terminals that display whatever they print.
func Close() {
	if globalLogFile != nil {
		_ = globalLogFile.Close()
	}
}

// FileName is where logs are written.
func FileName() string {
	return logFileName
}
