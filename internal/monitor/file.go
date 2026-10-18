package monitor

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// File is the YAML format for monitors defined at startup:
//
//	monitors:
//	  - name: homepage
//	    type: http
//	    target: https://example.com
type File struct {
	Monitors []Spec `yaml:"monitors"`
}

// LoadFile reads, normalizes and validates a monitors file. Unknown keys are
// rejected so that typos fail loudly instead of being silently ignored.
func LoadFile(path string) ([]Spec, error) {
	b, err := os.ReadFile(path) //nolint:gosec // path comes from operator config
	if err != nil {
		return nil, err
	}
	return ParseFile(b)
}

func ParseFile(b []byte) ([]Spec, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var f File
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse monitors file: %w", err)
	}
	var errs []error
	seen := map[string]bool{}
	for i := range f.Monitors {
		s := &f.Monitors[i]
		s.Normalize()
		if err := s.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("monitor %d (%q): %w", i+1, s.Name, err))
		}
		if seen[s.Name] {
			errs = append(errs, fmt.Errorf("monitor %d: duplicate name %q", i+1, s.Name))
		}
		seen[s.Name] = true
	}
	return f.Monitors, errors.Join(errs...)
}
