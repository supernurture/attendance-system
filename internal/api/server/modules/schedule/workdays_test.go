package schedule

import (
	"slices"
	"testing"
)

func TestPqInt16ArrayScan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  any
		want pqInt16Array
		bad  bool
	}{
		{name: "text literal", src: "{1,2,3}", want: pqInt16Array{1, 2, 3}},
		{name: "bytes literal", src: []byte("{1,7}"), want: pqInt16Array{1, 7}},
		{name: "spaces", src: " { 1 , 2 } ", want: pqInt16Array{1, 2}},
		{name: "empty array", src: "{}"},
		{name: "wrong type", src: 7, bad: true},
		{name: "not a number", src: "{a}", bad: true},
		{name: "out of int16 range", src: "{40000}", bad: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var got pqInt16Array
			err := got.Scan(test.src)
			if test.bad {
				if err == nil {
					t.Fatalf("Scan(%v) should fail", test.src)
				}
				return
			}
			if err != nil {
				t.Fatalf("Scan(%v): %v", test.src, err)
			}
			if !slices.Equal(got, test.want) {
				t.Errorf("Scan(%v) = %v, want %v", test.src, got, test.want)
			}
		})
	}
}

func TestPqInt16ArrayValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		array pqInt16Array
		want  string
	}{
		{name: "weekdays", array: pqInt16Array{1, 2, 3, 4, 5}, want: "{1,2,3,4,5}"},
		{name: "one day", array: pqInt16Array{7}, want: "{7}"},
		{name: "empty", array: nil, want: "{}"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			value, err := test.array.Value()
			if err != nil {
				t.Fatalf("Value: %v", err)
			}
			if value != test.want {
				t.Errorf("Value() = %v, want %v", value, test.want)
			}
		})
	}
}
