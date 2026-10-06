package geometry

import "testing"

func TestArea(t *testing.T) {
	cases := []struct {
		name  string
		shape Shape
		want  int
	}{
		{"square", Shape{Width: 4, Height: 4}, 16},
		{"rectangle", Shape{Width: 3, Height: 5}, 15},
		{"negative width", Shape{Width: -1, Height: 5}, 0},
		{"negative height", Shape{Width: 5, Height: -1}, 0},
		{"both negative", Shape{Width: -2, Height: -2}, 0},
		{"zero", Shape{}, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Area(tc.shape); got != tc.want {
				t.Errorf("Area(%+v) = %d, want %d", tc.shape, got, tc.want)
			}
		})
	}
}

func TestPerimeter(t *testing.T) {
	if got := Perimeter(Shape{Width: 3, Height: 5}); got != 16 {
		t.Errorf("Perimeter = %d, want 16", got)
	}
	if got := Perimeter(Shape{Width: -3, Height: 5}); got != 0 {
		t.Errorf("a negative dimension must yield 0, got %d", got)
	}
}

func TestFits(t *testing.T) {
	outer := Shape{Width: 10, Height: 10}

	if !Fits(Shape{Width: 5, Height: 5}, outer, 0) {
		t.Error("a 5x5 shape fits inside a 10x10 one")
	}
	if Fits(Shape{Width: 10, Height: 5}, outer, 1) {
		t.Error("a 10x5 shape does not fit inside a 10x10 one with a margin of 1")
	}
	if Fits(Shape{Width: 5, Height: 5}, outer, -1) {
		t.Error("a negative margin must not be treated as generous")
	}
}

func TestDescribe(t *testing.T) {
	cases := map[string]Shape{
		"empty":  {},
		"square": {Width: 4, Height: 4},
		"wide":   {Width: 8, Height: 2},
		"tall":   {Width: 2, Height: 8},
	}
	for want, shape := range cases {
		if got := Describe(shape); got != want {
			t.Errorf("Describe(%+v) = %q, want %q", shape, got, want)
		}
	}
}
