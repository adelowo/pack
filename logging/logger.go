package logging

import (
	"fmt"
	"github.com/buildpack/pack/style"
	"github.com/fatih/color"
	"io"
	"io/ioutil"
	"log"
)

type Logger struct {
	verbose bool
	out     *logWriter
	err     *logWriter
}

func NewLogger(stdout, stderr io.Writer, verbose, timestamps bool) *Logger {
	return &Logger{
		verbose: verbose,
		out:     newLogWriter(stdout, timestamps),
		err:     newLogWriter(stderr, timestamps),
	}
}

func (l *Logger) printf(w *logWriter, format string, a ...interface{}) {
	w.Write([]byte(fmt.Sprintf(format+"\n", a...)))
}

func (l *Logger) Info(format string, a ...interface{}) {
	l.printf(l.out, format, a...)
}

func (l *Logger) Verbose(format string, a ...interface{}) {
	if l.verbose {
		l.printf(l.out, format, a...)
	}
}

func (l *Logger) Error(format string, a ...interface{}) {
	l.printf(l.err, style.Error("ERROR: ")+format, a...)
}

func (l *Logger) Tip(format string, a ...interface{}) {
	l.printf(l.out, style.Tip("Tip: ")+format, a...)
}

func (l *Logger) VerboseWriter() *logWriter {
	if !l.verbose {
		return nullLogWriter
	}
	return l.out
}

func (l *Logger) VerboseErrorWriter() *logWriter {
	if !l.verbose {
		return nullLogWriter
	}
	return l.err
}

type logWriter struct {
	prefix string
	log    *log.Logger
}

var nullLogWriter = newLogWriter(ioutil.Discard, false)

func newLogWriter(out io.Writer, timestamps bool) *logWriter {
	flags := 0
	timestampStart := ""
	timestampEnd := ""
	if !color.NoColor {
		// Go logger prefixes appear before timestamp, so insert color start/end sequences around timestamp
		timestampStart = fmt.Sprintf("\x1b[%dm", style.TimestampColorCode)
		timestampEnd = fmt.Sprintf("\x1b[%dm", color.Reset)
	}
	prefix := ""
	if timestamps {
		flags = log.LstdFlags
		prefix = " "
	}

	return &logWriter{
		prefix: timestampEnd + prefix,
		log:    log.New(out, timestampStart, flags),
	}
}

func (w *logWriter) WithPrefix(prefix string) *logWriter {
	return &logWriter{
		log:    w.log,
		prefix: fmt.Sprintf("%s[%s] ", w.prefix, style.Prefix(prefix)),
	}
}

func (w *logWriter) Write(p []byte) (n int, err error) {
	w.log.Print(w.prefix + string(p))
	return len(p), nil
}
