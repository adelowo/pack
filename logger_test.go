package pack_test

import (
	"bytes"
	"github.com/buildpack/pack/style"
	"regexp"
	"testing"

	"github.com/fatih/color"
	"github.com/sclevine/spec"
	"github.com/sclevine/spec/report"

	"github.com/buildpack/pack"
	h "github.com/buildpack/pack/testhelpers"
)

func TestLogger(t *testing.T) {
	color.NoColor = false // IMPORTANT: Keep this to avoid false positive tests
	spec.Run(t, "Logger", testLogger, spec.Parallel(), spec.Report(report.Terminal{}))
}

func testLogger(t *testing.T, when spec.G, it spec.S) {

	var (
		logger  *pack.Logger
		outBuf  bytes.Buffer
		errBuff bytes.Buffer
	)

	when("verbose", func() {
		when("logger has verbose enabled", func() {
			it.Before(func() {
				logger = pack.NewLogger(&outBuf, &errBuff, true, false)
			})

			it("shows verbose output", func() {
				logger.Verbose("Some verbose output")

				h.AssertEq(t, outBuf.String(), "Some verbose output\n")
			})
		})

		when("logger has debug disabled", func() {
			it.Before(func() {
				logger = pack.NewLogger(&outBuf, &errBuff, false, false)
			})

			it("does not show verbose output", func() {
				logger.Verbose("Some verbose output")

				h.AssertEq(t, outBuf.String(), "")
			})
		})
	})

	when("timestamps", func() {
		when("logger has timestamps enabled", func() {
			it.Before(func() {
				logger = pack.NewLogger(&outBuf, &errBuff, false, true)
			})

			it("prefixes logging with timestamp", func() {
				logger.Info("Some text")

				h.AssertMatch(t, outBuf.String(), regexp.MustCompile(
					`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} \Q`+style.Separator("| ")+`\ESome text`))
			})
		})

		when("logger has timestamps disabled", func() {
			it.Before(func() {
				logger = pack.NewLogger(&outBuf, &errBuff, false, false)
			})

			it("does not prefix logging with timestamp", func() {
				logger.Info("Some text")

				h.AssertEq(t, outBuf.String(), "Some text\n")
			})
		})
	})

	when("styling", func() {
		it.Before(func() {
			logger = pack.NewLogger(&outBuf, &errBuff, true, false)
		})

		when("#Info", func() {
			it("displays unstyled info message", func() {
				logger.Info("This is some info")

				h.AssertEq(t, outBuf.String(), "This is some info\n")
			})
		})

		when("#Verbose", func() {
			it("displays unstyled verbose message", func() {
				logger.Verbose("This is some verbose text")

				h.AssertEq(t, outBuf.String(), "This is some verbose text\n")
			})
		})

		when("#Error", func() {
			it("displays styled error message to error buffer", func() {
				logger.Error("Something went wrong!")

				h.AssertEq(t, errBuff.String(), style.Error("ERROR: ")+"Something went wrong!\n")
			})
		})

		when("#Tip", func() {
			it("displays styled tip message", func() {
				logger.Tip("This is a tip")

				h.AssertEq(t, outBuf.String(), style.Tip("Tip: ")+"This is a tip\n")
			})
		})
	})
}
