package earcut

import "math"

// Deviation returns the relative difference between the area of the polygon and the
// total area of the triangles in indices. A value near zero means the triangulation
// is correct. It takes the same polygon and holes that were passed to Triangulate.
func Deviation[T float](polygon []Point[T], holes [][]Point[T], indices []uint32) float64 {
	polygonArea := math.Abs(signedArea(polygon))
	for _, hole := range holes {
		polygonArea -= math.Abs(signedArea(hole))
	}

	points := concatRings(polygon, holes)

	var trianglesArea float64
	for i := 0; i+2 < len(indices); i += 3 {
		a := points[indices[i]]
		b := points[indices[i+1]]
		c := points[indices[i+2]]

		ax, ay := float64(a.X), float64(a.Y)
		bx, by := float64(b.X), float64(b.Y)
		cx, cy := float64(c.X), float64(c.Y)

		// do not remove the extra float64 casts, they prevent go from emitting fma instructions
		//goland:noinspection GoRedundantConversion
		trianglesArea += math.Abs(float64((ax-cx)*(by-ay)) - float64((ax-bx)*(cy-ay)))
	}

	// with no triangles, a polygon area within shoelace roundoff of zero (which scales with the
	// squared coordinate magnitude) means the input was degenerate rather than wrongly triangulated
	if trianglesArea == 0 {
		var maxCoord float64
		for _, p := range points {
			maxCoord = max(maxCoord, math.Abs(float64(p.X)), math.Abs(float64(p.Y)))
		}

		// upstream counts coordinates, not points
		limit := float64(2*len(points)) * maxCoord * maxCoord * epsilon
		if math.Abs(polygonArea) <= limit {
			return 0
		}

		return 1
	}

	return math.Abs((trianglesArea - polygonArea) / polygonArea)
}

// javascript's Number.EPSILON
const epsilon = 0x1p-52
