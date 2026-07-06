package shared

import (
	"testing"

	"github.com/go-playground/validator/v10"
)

func TestRuneCountLen(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{
			name:     "empty string",
			input:    "",
			expected: 0,
		},
		{
			name:     "ASCII string",
			input:    "hello",
			expected: 5,
		},
		{
			name:     "precomposed é (single code point)",
			input:    "héllo", // é is U+00E9
			expected: 5,
		},
		{
			name:     "decomposed e + combining acute (two code points)",
			input:    "he\u0301llo", // e + combining acute accent
			expected: 6,             // This is the rune-vs-grapheme distinction
		},
		{
			name:     "two emoji (8 bytes, 2 runes)",
			input:    "😀😀",
			expected: 2,
		},
		{
			name:     "100 two-byte characters",
			input:    "éééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééééé",
			expected: 100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RuneCountLen(tt.input)
			if got != tt.expected {
				t.Errorf("RuneCountLen(%q) = %d, want %d (len=%d bytes)", tt.input, got, tt.expected, len(tt.input))
			}
		})
	}
}

// TestValidatePagination_DefaultLimitWhenOmitted asserts that the limit
// applied when ?limit is omitted equals DefaultPageSize, the single source of
// truth shared with the OpenAPI spec (see internal/contract) and the userlog
// service. If someone changes DefaultPageSize, this test follows it; if someone
// reintroduces a divergent literal default, the contract test catches it.
func TestValidatePagination_DefaultLimitWhenOmitted(t *testing.T) {
	limit, offset, err := ValidatePagination("", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if limit != DefaultPageSize {
		t.Errorf("default limit: got %d want DefaultPageSize (%d)", limit, DefaultPageSize)
	}
	if offset != 0 {
		t.Errorf("default offset: got %d want 0", offset)
	}
}

// TestValidatePagination_ClampsToMax asserts a request over MaxPageSize is
// clamped to MaxPageSize rather than rejected.
func TestValidatePagination_ClampsToMax(t *testing.T) {
	limit, _, err := ValidatePagination("100000", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if limit != MaxPageSize {
		t.Errorf("clamped limit: got %d want MaxPageSize (%d)", limit, MaxPageSize)
	}
}

func TestRegisterRuneLenValidators(t *testing.T) {
	v := validator.New()
	err := RegisterRuneLenValidators(v)
	if err != nil {
		t.Fatalf("RegisterRuneLenValidators returned error: %v", err)
	}
}

func TestRuneMaxValidator(t *testing.T) {
	v := validator.New()
	if err := RegisterRuneLenValidators(v); err != nil {
		t.Fatal(err)
	}

	type testStruct struct {
		S string `validate:"rune_max=3"`
	}

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "3 ASCII chars - valid",
			input:   "abc",
			wantErr: false,
		},
		{
			name:    "4 ASCII chars - invalid",
			input:   "abcd",
			wantErr: true,
		},
		{
			name:    "3 emoji (12 bytes, 3 runes) - valid",
			input:   "😀😀😀",
			wantErr: false,
		},
		{
			name:    "4 emoji (16 bytes, 4 runes) - invalid",
			input:   "😀😀😀😀",
			wantErr: true,
		},
		{
			name:    "3 two-byte chars (6 bytes, 3 runes) - valid",
			input:   "ééé",
			wantErr: false,
		},
		{
			name:    "empty string - valid",
			input:   "",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testStruct{S: tt.input}
			err := v.Struct(s)
			if (err != nil) != tt.wantErr {
				t.Errorf("validate(%q): error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestRuneMinValidator(t *testing.T) {
	v := validator.New()
	if err := RegisterRuneLenValidators(v); err != nil {
		t.Fatal(err)
	}

	type testStruct struct {
		S string `validate:"rune_min=3"`
	}

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "3 ASCII chars - valid",
			input:   "abc",
			wantErr: false,
		},
		{
			name:    "2 ASCII chars - invalid",
			input:   "ab",
			wantErr: true,
		},
		{
			name:    "3 emoji - valid",
			input:   "😀😀😀",
			wantErr: false,
		},
		{
			name:    "2 emoji - invalid",
			input:   "😀😀",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testStruct{S: tt.input}
			err := v.Struct(s)
			if (err != nil) != tt.wantErr {
				t.Errorf("validate(%q): error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestRuneLenValidator(t *testing.T) {
	v := validator.New()
	if err := RegisterRuneLenValidators(v); err != nil {
		t.Fatal(err)
	}

	type testStruct struct {
		S string `validate:"rune_len=3"`
	}

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "exactly 3 ASCII chars - valid",
			input:   "abc",
			wantErr: false,
		},
		{
			name:    "2 ASCII chars - invalid",
			input:   "ab",
			wantErr: true,
		},
		{
			name:    "4 ASCII chars - invalid",
			input:   "abcd",
			wantErr: true,
		},
		{
			name:    "3 emoji - valid",
			input:   "😀😀😀",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testStruct{S: tt.input}
			err := v.Struct(s)
			if (err != nil) != tt.wantErr {
				t.Errorf("validate(%q): error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestRuneMaxWithOmitempty(t *testing.T) {
	v := validator.New()
	if err := RegisterRuneLenValidators(v); err != nil {
		t.Fatal(err)
	}

	type testStruct struct {
		S string `validate:"omitempty,rune_max=3"`
	}

	// Empty string should pass with omitempty
	s := testStruct{S: ""}
	err := v.Struct(s)
	if err != nil {
		t.Errorf("empty string with omitempty should pass, got error: %v", err)
	}
}
