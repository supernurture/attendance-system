package report

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/johnfercher/maroto/v2"
	"github.com/johnfercher/maroto/v2/pkg/components/row"
	"github.com/johnfercher/maroto/v2/pkg/components/text"
	"github.com/johnfercher/maroto/v2/pkg/config"
	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
	"github.com/johnfercher/maroto/v2/pkg/core"
	"github.com/johnfercher/maroto/v2/pkg/props"
)

// generate is a seam: rendering plain text never fails, so only a test can make it.
var generate = core.Maroto.Generate

func writeCSV(report Report) []byte {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	_ = w.Write(csvHeader) // a bytes.Buffer never fails a write
	for _, day := range report.Days {
		row := []string{
			strconv.FormatInt(day.UserID, 10), cell(day.FullName), optionalID(day.DepartmentID),
			day.Date.Format(time.DateOnly), string(day.Status), "", "", "0", "0", strconv.Itoa(worked(day)),
		}
		if held := day.Attendance; held != nil {
			row[5] = held.CheckInAt.UTC().Format(time.RFC3339)
			if held.CheckOutAt != nil {
				row[6] = held.CheckOutAt.UTC().Format(time.RFC3339)
			}
			row[7], row[8] = strconv.Itoa(held.LateMinutes), strconv.Itoa(held.EarlyLeaveMinutes)
		}
		_ = w.Write(row)
	}
	w.Flush()
	return buf.Bytes()
}

func writePDF(report Report) ([]byte, error) {
	doc := maroto.New(config.NewBuilder().WithPageNumber().Build())
	bold := props.Text{Style: fontstyle.Bold}
	doc.AddRows(
		text.NewRow(10, "Attendance summary", props.Text{Size: 14, Style: fontstyle.Bold}),
		text.NewRow(8, fmt.Sprintf("%s to %s",
			report.Span.From.Format(time.DateOnly), report.Span.To.Format(time.DateOnly))),
		line(pdfHeader, bold),
	)
	for _, total := range report.People {
		doc.AddRows(line([]string{
			total.FullName, strconv.Itoa(total.Present), strconv.Itoa(total.Late), strconv.Itoa(total.OnLeave),
			strconv.Itoa(total.Absent), hours(total.WorkedMinutes),
		}, props.Text{}))
	}

	rendered, err := generate(doc)
	if err != nil {
		return nil, err
	}
	return rendered.GetBytes(), nil
}

func line(values []string, style props.Text) core.Row {
	cols := make([]core.Col, len(values))
	for i, value := range values {
		cols[i] = text.NewCol(pdfColumns[i], value, style)
	}
	return row.New().Add(cols...) // grows with a name that wraps
}

// hours is minutes as h:mm.
func hours(minutes int) string {
	return fmt.Sprintf("%d:%02d", minutes/60, minutes%60)
}

// cell keeps a spreadsheet from running a name as a formula.
func cell(value string) string {
	if value != "" && strings.ContainsRune("=+-@\t\r", rune(value[0])) {
		return "'" + value
	}
	return value
}

func optionalID(id *int64) string {
	if id == nil {
		return ""
	}
	return strconv.FormatInt(*id, 10)
}
