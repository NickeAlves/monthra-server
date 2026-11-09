package email

import (
	"fmt"
	"strconv"
	"time"

	"github.com/NickeAlves/monthra-server/internal/i18n"
)

// monthNames are the names of the months in each locale, January first.
var monthNames = map[i18n.Locale][12]string{
	i18n.PtBR: {"janeiro", "fevereiro", "março", "abril", "maio", "junho", "julho", "agosto", "setembro", "outubro", "novembro", "dezembro"},
	i18n.EsES: {"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio", "agosto", "septiembre", "octubre", "noviembre", "diciembre"},
	i18n.EnUS: {"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"},
}

// monthName writes month ("YYYY-MM") out in locale l: "novembro de 2026",
// "noviembre de 2026", "November 2026". An empty month is "".
func monthName(month string, l i18n.Locale) (string, error) {
	if month == "" {
		return "", nil
	}
	t, err := time.Parse("2006-01", month)
	if err != nil {
		return "", fmt.Errorf("invalid email month %q", month)
	}
	names, ok := monthNames[l]
	if !ok {
		names = monthNames[i18n.Default]
	}
	name, year := names[t.Month()-1], strconv.Itoa(t.Year())
	if l == i18n.EnUS {
		return name + " " + year, nil
	}
	return name + " de " + year, nil
}
