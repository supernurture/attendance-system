package report

import (
	"errors"
	"regexp"
	"strconv"
	"testing"

	"github.com/johnfercher/maroto/v2/pkg/core"
)

func TestCell(t *testing.T) {
	tests := map[string]string{
		"":             "",
		"Ani":          "Ani",
		"=HYPERLINK()": "'=HYPERLINK()",
		"+1":           "'+1",
		"-1":           "'-1",
		"@SUM(A1)":     "'@SUM(A1)",
	}
	for value, want := range tests {
		if got := cell(value); got != want {
			t.Errorf("cell(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestHours(t *testing.T) {
	if got := hours(1050); got != "17:30" {
		t.Errorf("hours(1050) = %q, want 17:30", got)
	}
	if got := hours(5); got != "0:05" {
		t.Errorf("hours(5) = %q, want 0:05", got)
	}
}

func TestOptionalID(t *testing.T) {
	id := int64(42)
	if optionalID(nil) != "" || optionalID(&id) != "42" {
		t.Errorf("optionalID = %q and %q, want empty and 42", optionalID(nil), optionalID(&id))
	}
}

func TestWritePDFPassesAFailureOn(t *testing.T) {
	original := generate
	generate = func(core.Maroto) (core.Document, error) { return nil, errInjected }
	t.Cleanup(func() { generate = original })

	if _, err := writePDF(Report{}); !errors.Is(err, errInjected) {
		t.Errorf("writePDF = %v, want the injected failure", err)
	}
}

func TestWritePDFGrowsARowForAWrappedName(t *testing.T) {
	long := "Raden Mas Muhammad Abdurrahman Wicaksono Hadiningrat Putra"
	pdf, err := writePDF(Report{People: []Total{{FullName: long}, {FullName: "Budi"}}})
	if err != nil {
		t.Fatalf("writePDF: %v", err)
	}

	// Baselines grow downward from the top of the page, so a lower line has a smaller y.
	baseline := map[string]float64{}
	for _, match := range regexp.MustCompile(`([\d.]+) Td \(([^)]*)\) Tj`).FindAllSubmatch(pdf, -1) {
		baseline[string(match[2])], _ = strconv.ParseFloat(string(match[1]), 64)
	}
	lowest := baseline["Wicaksono Hadiningrat Putra"]
	if lowest == 0 || baseline["Budi"] > lowest-10 {
		t.Errorf("baselines %v: the next row must start below the wrapped name's last line", baseline)
	}
}
