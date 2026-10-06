package geometry

// Shape is the example domain type.
type Shape struct {
	Width  int
	Height int
}

// Area returns the shape's area, or zero for a negative dimension.
//
// The guard is the point of the function: a naive multiplication would return a
// negative area, which is how geometry code acquires silent bugs.
func Area(s Shape) int {
	if s.Width < 0 || s.Height < 0 {
		return 0
	}
	return s.Width * s.Height
}

// Perimeter returns the shape's perimeter.
func Perimeter(s Shape) int {
	if s.Width < 0 || s.Height < 0 {
		return 0
	}
	return 2 * (s.Width + s.Height)
}

// Fits reports whether one shape fits inside another with a margin.
func Fits(inner, outer Shape, margin int) bool {
	if margin < 0 {
		return false
	}
	if inner.Width+margin > outer.Width {
		return false
	}
	return inner.Height+margin <= outer.Height
}

// Describe returns a short human description of the shape.
func Describe(s Shape) string {
	switch {
	case s.Width == 0 && s.Height == 0:
		return "empty"
	case s.Width == s.Height:
		return "square"
	case s.Width > s.Height:
		return "wide"
	default:
		return "tall"
	}
}
