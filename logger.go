package pack

import (
	"github.com/buildpack/pack/style"
	"io"
	"log"
)

type Logger struct {
	verbose bool
	prefix  string
	out     *log.Logger
	err     *log.Logger
}

func NewLogger(stdout, stderr io.Writer, verbose, noTimestamps bool) *Logger {
	flags := log.LstdFlags
	prefix := style.Separator("| ")

	if noTimestamps {
		flags = 0
		prefix = ""
	}

	return &Logger{
		verbose: verbose,
		prefix:  prefix,
		out:     log.New(stdout, "", flags),
		err:     log.New(stderr, "", flags),
	}
}

func (l *Logger) printf(log *log.Logger, newline bool, format string, a ...interface{}) {
	ending := ""
	if newline {
		ending = "\n"
	}
	log.Printf(l.prefix+format+ending, a...)
}

func (l *Logger) Info(format string, a ...interface{}) {
	l.printf(l.out, true, format, a...)
}

func (l *Logger) Error(format string, a ...interface{}) {
	l.printf(l.err, true, style.Error("ERROR: ")+format, a...)
}

func (l *Logger) Debug(format string, a ...interface{}) {
	if l.verbose {
		l.printf(l.out, true, format, a...)
	}
}

func (l *Logger) Tip(format string, a ...interface{}) {
	l.printf(l.out, true, style.Tip("Tip: ")+format, a...)
}
