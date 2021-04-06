package headers

import (
	"fmt"
	"strings"
)

func findValue(str string, separator byte) (string, string, error) {
	if str == "" {
		return "", "", nil
	}

	if str[0] == '"' {
		i := 1
		for {
			if i >= len(str) {
				return "", "", fmt.Errorf("apices not closed (%v)", str)
			}

			if str[i] == '"' {
				return str[1:i], str[i+1:], nil
			}

			i++
		}
	}

	i := 0
	for {
		if i >= len(str) || str[i] == separator {
			return str[:i], str[i:], nil
		}

		i++
	}
}

func keyValParse(str string, separator byte) (map[string]string, error) {
	ret := make(map[string]string)

	for len(str) > 0 {
		i := strings.IndexByte(str, '=')
		if i < 0 {
			return nil, fmt.Errorf("unable to find key")
		}
		var k string
		k, str = str[:i], str[i+1:]

		var v string
		var err error
		v, str, err = findValue(str, separator)
		if err != nil {
			return nil, err
		}

		ret[k] = v

		// skip separator
		if len(str) > 0 && str[0] == separator {
			str = str[1:]
		}

		// skip spaces
		for len(str) > 0 && str[0] == ' ' {
			str = str[1:]
		}
	}

	return ret, nil
}
