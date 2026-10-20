package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/itsgitz/maket/apps/api/internal/apitypes"
)

// humanSize formats n bytes 1024-based with at most one decimal:
// "820 B", "15 KB", "1.2 MB".
func humanSize(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " B"
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	v := float64(n) / 1024
	i := 0
	for i < len(units)-1 && roundTenth(v) >= 1024 {
		v /= 1024
		i++
	}
	s := strconv.FormatFloat(roundTenth(v), 'f', 1, 64)
	return strings.TrimSuffix(s, ".0") + " " + units[i]
}

func roundTenth(v float64) float64 {
	r, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'f', 1, 64), 64)
	return r
}

// updatedLayout is how ls prints a time, in the local zone.
const updatedLayout = "2006-01-02 15:04"

// writeTable prints Prototypes as an aligned table.
func writeTable(w io.Writer, protos []apitypes.Prototype) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SLUG\tNAME\tVERSION\tSIZE\tUPDATED\tURL")
	for _, p := range protos {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\n",
			cell(p.Slug), cell(p.Name), p.Version, humanSize(p.SizeBytes),
			p.UpdatedAt.In(time.Local).Format(updatedLayout), cell(p.URL))
	}
	tw.Flush()
}

// cell makes s safe for one table cell: tabs, newlines and other control
// characters become spaces.
func cell(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}
