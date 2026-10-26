package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// loadDotEnv reads KEY=VALUE pairs from a file and sets the variables that are
// not in the environment yet: a value set in the shell wins over the file
// a missing file is not an error
func loadDotEnv(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	vars, err := parseDotEnv(data)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	for k, v := range vars {
		if cur, ok := os.LookupEnv(k); ok && cur != "" {
			continue
		}

		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("set env %s: %w", k, err)
		}
	}

	return nil
}

// parseDotEnv supports blank lines, "#" comments, quoted values and a comment
// after an unquoted value: KEY=value # comment
func parseDotEnv(data []byte) (map[string]string, error) {
	vars := map[string]string{}

	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, val, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")

		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("line %d: want KEY=VALUE", n)
		}

		val = strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		} else if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}

		vars[key] = val
	}

	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}

	return vars, nil
}
