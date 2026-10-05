package log

import (
	"fmt"
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
func Initialize(tag string) {
	f, err := os.OpenFile(logFileName, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		panic(fmt.Sprintf("could not open log file: %s", err))
	}

	// Set log format to include timestamp and file/line number
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	InfoLog = log.New(f, tag+"INFO:", log.Ldate|log.Ltime|log.Lshortfile)
	WarningLog = log.New(f, tag+"WARNING:", log.Ldate|log.Ltime|log.Lshortfile)
	ErrorLog = log.New(f, tag+"ERROR:", log.Ldate|log.Ltime|log.Lshortfile)

	globalLogFile = f
}

// Close closes the log file. It prints nothing: the hook and status line
// commands write to terminals that display whatever they print.
func Close() {
	_ = globalLogFile.Close()
}

// FileName is where logs are written.
func FileName() string {
	return logFileName
}
