package shell

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// dateCommand is date over the clock now, always in UTC.
func dateCommand(now func() time.Time) Command {
	return Command{
		Name:    "date",
		Summary: "print the time in UTC (+FORMAT, -u, -d @seconds|date, -I[date|hours|minutes|seconds|ns], -R)",
		Run: func(_ context.Context, invocation *Invocation) error {
			return runDate(invocation, now().UTC())
		},
	}
}

func runDate(invocation *Invocation, now time.Time) error {
	parsed, operands, err := optionSet{flags: "uR", valued: "dI", long: map[string]longOption{
		"utc": {name: "u"}, "universal": {name: "u"}, "rfc-email": {name: "R"},
		"date": {name: "d", valued: true}, "iso-8601": {name: "I", valued: true},
	}}.parse(normalizeISOFlag(invocation.Arguments))
	if err != nil {
		return usage(invocation, "date", err, errFailed)
	}
	moment, format := now, "%a %b %e %H:%M:%S %Z %Y"
	for _, given := range parsed {
		switch given.name {
		case "d":
			if moment, err = parseDate(given.value, now); err != nil {
				complain(invocation, "date", "invalid date '%s'", given.value)
				return errFailed
			}
		case "R":
			format = "%a, %d %b %Y %H:%M:%S %z"
		case "I":
			if format, err = isoFormat(given.value); err != nil {
				complain(invocation, "date", "%v", err)
				return errFailed
			}
		}
	}
	switch {
	case len(operands) > 1 || (len(operands) == 1 && !strings.HasPrefix(operands[0], "+")):
		return usage(invocation, "date", fmt.Errorf("extra operand '%s'", operands[len(operands)-1]), errFailed)
	case len(operands) == 1:
		format = operands[0][1:]
	}
	text, err := formatDate(moment, format)
	if err != nil {
		complain(invocation, "date", "%v", err)
		return errFailed
	}
	_, err = fmt.Fprintln(invocation.Stdout, text)
	return err
}

// normalizeISOFlag gives a bare -I (or --iso-8601) its default value, so
// the option parser can treat the flag as valued like GNU's optional
// argument.
func normalizeISOFlag(arguments []string) []string {
	normalized := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		switch argument {
		case "-I", "--iso-8601":
			argument = "-Idate"
		}
		normalized = append(normalized, argument)
	}
	return normalized
}

func isoFormat(precision string) (string, error) {
	switch precision {
	case "date":
		return "%Y-%m-%d", nil
	case "hours":
		return "%Y-%m-%dT%H%:z", nil
	case "minutes":
		return "%Y-%m-%dT%H:%M%:z", nil
	case "seconds":
		return "%Y-%m-%dT%H:%M:%S%:z", nil
	case "ns":
		return "%Y-%m-%dT%H:%M:%S,%N%:z", nil
	}
	return "", fmt.Errorf("invalid argument '%s' for '--iso-8601'", precision)
}

// parseDate reads the -d forms the shell supports: @seconds, "now", and
// ISO dates with an optional time.
func parseDate(text string, now time.Time) (time.Time, error) {
	if seconds, ok := strings.CutPrefix(text, "@"); ok {
		value, err := strconv.ParseInt(seconds, 10, 64)
		if err != nil {
			return time.Time{}, err
		}
		return time.Unix(value, 0).UTC(), nil
	}
	if text == "now" {
		return now, nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid date %q", text)
}

// formatDate expands a GNU date format (C locale); an unknown conversion is
// an error rather than a silent difference.
func formatDate(moment time.Time, format string) (string, error) {
	var text strings.Builder
	for index := 0; index < len(format); index++ {
		if format[index] != '%' || index == len(format)-1 {
			text.WriteByte(format[index])
			continue
		}
		index++
		conversion := string(format[index])
		if format[index] == ':' && index+1 < len(format) && format[index+1] == 'z' {
			index++
			conversion = ":z"
		}
		expanded, ok := dateConversion(moment, conversion)
		if !ok {
			return "", fmt.Errorf("the format conversion %%%s is not supported", conversion)
		}
		text.WriteString(expanded)
	}
	return text.String(), nil
}

// dateConversion expands one conversion such as "Y" for %Y.
func dateConversion(moment time.Time, conversion string) (string, bool) {
	if layout, ok := dateLayout(conversion); ok {
		return moment.Format(layout), true
	}
	switch conversion {
	case "%":
		return "%", true
	case "n":
		return "\n", true
	case "t":
		return "\t", true
	case "s":
		return strconv.FormatInt(moment.Unix(), 10), true
	case "Y":
		return strconv.Itoa(moment.Year()), true
	case "u":
		return strconv.Itoa((int(moment.Weekday())+6)%7 + 1), true
	case "w":
		return strconv.Itoa(int(moment.Weekday())), true
	}
	return numericConversion(moment, conversion)
}

// dateLayout returns the Go time layout of the conversions it expresses
// directly.
func dateLayout(conversion string) (string, bool) {
	switch conversion {
	case "a":
		return "Mon", true
	case "A":
		return "Monday", true
	case "b", "h":
		return "Jan", true
	case "B":
		return "January", true
	case "c":
		return "Mon Jan _2 15:04:05 2006", true
	case "D":
		return "01/02/06", true
	case "F":
		return "2006-01-02", true
	case "p":
		return "PM", true
	case "R":
		return "15:04", true
	case "T":
		return "15:04:05", true
	case "Z":
		return "MST", true
	case "z":
		return "-0700", true
	case ":z":
		return "-07:00", true
	}
	return "", false
}

// numericConversion expands the padded number conversions.
func numericConversion(moment time.Time, conversion string) (string, bool) {
	hour12 := moment.Hour() % 12
	if hour12 == 0 {
		hour12 = 12
	}
	switch conversion {
	case "C":
		return fmt.Sprintf("%02d", moment.Year()/100), true
	case "d":
		return fmt.Sprintf("%02d", moment.Day()), true
	case "e":
		return fmt.Sprintf("%2d", moment.Day()), true
	case "H":
		return fmt.Sprintf("%02d", moment.Hour()), true
	case "I":
		return fmt.Sprintf("%02d", hour12), true
	case "j":
		return fmt.Sprintf("%03d", moment.YearDay()), true
	case "k":
		return fmt.Sprintf("%2d", moment.Hour()), true
	case "l":
		return fmt.Sprintf("%2d", hour12), true
	case "m":
		return fmt.Sprintf("%02d", int(moment.Month())), true
	case "M":
		return fmt.Sprintf("%02d", moment.Minute()), true
	case "N":
		return fmt.Sprintf("%09d", moment.Nanosecond()), true
	case "S":
		return fmt.Sprintf("%02d", moment.Second()), true
	case "y":
		return fmt.Sprintf("%02d", moment.Year()%100), true
	}
	return "", false
}
