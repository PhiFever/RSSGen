package scraper

import (
	"testing"
)

func TestParseCookieString(t *testing.T) {
	tests := []struct {
		input    string
		expected map[string]string
	}{
		{
			input:    "key1=val1; key2=val2",
			expected: map[string]string{"key1": "val1", "key2": "val2"},
		},
		{
			input:    "",
			expected: map[string]string{},
		},
		{
			input:    "  key=val  ",
			expected: map[string]string{"key": "val"},
		},
		{
			input:    "key1=val1;key2=val2;key3=val3",
			expected: map[string]string{"key1": "val1", "key2": "val2", "key3": "val3"},
		},
		{
			input:    "key=val=with=equals",
			expected: map[string]string{"key": "val=with=equals"},
		},
	}

	for _, tt := range tests {
		result := ParseCookieString(tt.input)
		if len(result) != len(tt.expected) {
			t.Errorf("ParseCookieString(%q): len = %d, want %d", tt.input, len(result), len(tt.expected))
			continue
		}
		for k, v := range tt.expected {
			if result[k] != v {
				t.Errorf("ParseCookieString(%q)[%q] = %q, want %q", tt.input, k, result[k], v)
			}
		}
	}
}
